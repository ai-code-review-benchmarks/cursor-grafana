package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/snowflake"
	"github.com/grafana/grafana/pkg/apimachinery/utils"
	"github.com/grafana/grafana/pkg/storage/unified/resourcepb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultListBufferSize = 100
)

// Unified storage backend based on KV storage.
type kvStorageBackend struct {
	snowflake  *snowflake.Node
	kv         KV
	dataStore  *dataStore
	metaStore  *metadataStore
	eventStore *eventStore
	notifier   *notifier
	builder    DocumentBuilder
}

var _ StorageBackend = &kvStorageBackend{}

func NewkvStorageBackend(kv KV) *kvStorageBackend {
	s, err := snowflake.NewNode(rand.Int64N(1024))
	if err != nil {
		panic(err)
	}
	eventStore := newEventStore(kv)
	return &kvStorageBackend{
		kv:         kv,
		dataStore:  newDataStore(kv),
		metaStore:  newMetadataStore(kv),
		eventStore: eventStore,
		notifier:   newNotifier(eventStore, notifierOptions{}),
		snowflake:  s,
		builder:    StandardDocumentBuilder(), // For now we use the standard document builder.
	}
}

// WriteEvent writes a resource event (create/update/delete) to the storage backend.
func (k *kvStorageBackend) WriteEvent(ctx context.Context, event WriteEvent) (int64, error) {
	if err := event.Validate(); err != nil {
		return 0, fmt.Errorf("invalid event: %w", err)
	}
	rv := k.snowflake.Generate().Int64()

	// Write data.
	var action DataAction
	switch event.Type {
	case resourcepb.WatchEvent_ADDED:
		action = DataActionCreated
		// Check if resource already exists for create operations
		_, err := k.metaStore.GetLatestResourceKey(ctx, MetaGetRequestKey{
			Namespace: event.Key.Namespace,
			Group:     event.Key.Group,
			Resource:  event.Key.Resource,
			Name:      event.Key.Name,
		})
		if err == nil {
			// Resource exists, return already exists error
			return 0, ErrResourceAlreadyExists
		}
		if err != ErrNotFound {
			// Some other error occurred
			return 0, fmt.Errorf("failed to check if resource exists: %w", err)
		}
	case resourcepb.WatchEvent_MODIFIED:
		action = DataActionUpdated
	case resourcepb.WatchEvent_DELETED:
		action = DataActionDeleted
	default:
		return 0, fmt.Errorf("invalid event type: %d", event.Type)
	}

	// Build the search document
	doc, err := k.builder.BuildDocument(ctx, event.Key, rv, event.Value)
	if err != nil {
		return 0, fmt.Errorf("failed to build document: %w", err)
	}

	// Write the data
	err = k.dataStore.Save(ctx, DataKey{
		Namespace:       event.Key.Namespace,
		Group:           event.Key.Group,
		Resource:        event.Key.Resource,
		Name:            event.Key.Name,
		ResourceVersion: rv,
		Action:          action,
	}, io.NopCloser(bytes.NewReader(event.Value)))
	if err != nil {
		return 0, fmt.Errorf("failed to write data: %w", err)
	}

	// Write metadata
	err = k.metaStore.Save(ctx, MetaDataObj{
		Key: MetaDataKey{
			Namespace:       event.Key.Namespace,
			Group:           event.Key.Group,
			Resource:        event.Key.Resource,
			Name:            event.Key.Name,
			ResourceVersion: rv,
			Action:          action,
			Folder:          event.Object.GetFolder(),
		},
		Value: MetaData{
			IndexableDocument: *doc,
		},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to write metadata: %w", err)
	}

	// Write event
	err = k.eventStore.Save(ctx, Event{
		Namespace:       event.Key.Namespace,
		Group:           event.Key.Group,
		Resource:        event.Key.Resource,
		Name:            event.Key.Name,
		ResourceVersion: rv,
		Action:          action,
		Folder:          event.Object.GetFolder(),
		PreviousRV:      event.PreviousRV,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to save event: %w", err)
	}
	return rv, nil
}

func (k *kvStorageBackend) ReadResource(ctx context.Context, req *resourcepb.ReadRequest) *BackendReadResponse {
	if req.Key == nil {
		return &BackendReadResponse{Error: &resourcepb.ErrorResult{Code: http.StatusBadRequest, Message: "missing key"}}
	}
	meta, err := k.metaStore.GetResourceKeyAtRevision(ctx, MetaGetRequestKey{
		Namespace: req.Key.Namespace,
		Group:     req.Key.Group,
		Resource:  req.Key.Resource,
		Name:      req.Key.Name,
	}, req.ResourceVersion)
	if err == ErrNotFound {
		return &BackendReadResponse{Error: &resourcepb.ErrorResult{Code: http.StatusNotFound, Message: "not found"}}
	} else if err != nil {
		return &BackendReadResponse{Error: &resourcepb.ErrorResult{Code: http.StatusInternalServerError, Message: err.Error()}}
	}
	data, err := k.dataStore.Get(ctx, DataKey{
		Namespace:       req.Key.Namespace,
		Group:           req.Key.Group,
		Resource:        req.Key.Resource,
		Name:            req.Key.Name,
		ResourceVersion: meta.ResourceVersion,
		Action:          meta.Action,
	})
	if err != nil {
		return &BackendReadResponse{Error: &resourcepb.ErrorResult{Code: http.StatusInternalServerError, Message: err.Error()}}
	}
	value, err := io.ReadAll(data)
	if err != nil {
		return &BackendReadResponse{Error: &resourcepb.ErrorResult{Code: http.StatusInternalServerError, Message: err.Error()}}
	}
	return &BackendReadResponse{
		Key:             req.Key,
		ResourceVersion: meta.ResourceVersion,
		Value:           value,
		Folder:          meta.Folder,
	}
}

// // ListIterator returns an iterator for listing resources.
func (k *kvStorageBackend) ListIterator(ctx context.Context, req *resourcepb.ListRequest, cb func(ListIterator) error) (int64, error) {
	if req.Options == nil || req.Options.Key == nil {
		return 0, fmt.Errorf("missing options or key in ListRequest")
	}
	// Parse continue token if provided
	offset := int64(0)
	resourceVersion := req.ResourceVersion
	if req.NextPageToken != "" {
		token, err := GetContinueToken(req.NextPageToken)
		if err != nil {
			return 0, fmt.Errorf("invalid continue token: %w", err)
		}
		offset = token.StartOffset
		resourceVersion = token.ResourceVersion
	}

	// We set the listRV to the current time.
	listRV := k.snowflake.Generate().Int64()
	if resourceVersion > 0 {
		listRV = resourceVersion
	}

	// Fetch the latest objects
	keys := make([]MetaDataKey, 0, min(defaultListBufferSize, req.Limit+1))
	idx := 0
	for metaKey, err := range k.metaStore.ListResourceKeysAtRevision(ctx, MetaListRequestKey{
		Namespace: req.Options.Key.Namespace,
		Group:     req.Options.Key.Group,
		Resource:  req.Options.Key.Resource,
		Name:      req.Options.Key.Name,
	}, resourceVersion) {
		if err != nil {
			return 0, err
		}
		// Skip the first offset items. This is not efficient, but it's a simple way to implement it for now.
		if idx < int(offset) {
			idx++
			continue
		}
		keys = append(keys, metaKey)
		// Only fetch the first limit items + 1 to get the next token.
		if len(keys) >= int(req.Limit+1) {
			break
		}
	}
	iter := kvListIterator{
		keys:         keys,
		currentIndex: -1,
		ctx:          ctx,
		listRV:       listRV,
		offset:       offset,
		dataStore:    k.dataStore,
	}
	err := cb(&iter)
	if err != nil {
		return 0, err
	}

	return listRV, nil
}

// kvListIterator implements ListIterator for KV storage
type kvListIterator struct {
	ctx          context.Context
	keys         []MetaDataKey
	currentIndex int
	dataStore    *dataStore
	listRV       int64
	offset       int64

	// current
	rv    int64
	err   error
	value []byte
}

func (i *kvListIterator) Next() bool {
	i.currentIndex++

	if i.currentIndex >= len(i.keys) {
		return false
	}

	i.rv, i.err = i.keys[i.currentIndex].ResourceVersion, nil

	data, err := i.dataStore.Get(i.ctx, DataKey{
		Namespace:       i.keys[i.currentIndex].Namespace,
		Group:           i.keys[i.currentIndex].Group,
		Resource:        i.keys[i.currentIndex].Resource,
		Name:            i.keys[i.currentIndex].Name,
		ResourceVersion: i.keys[i.currentIndex].ResourceVersion,
		Action:          i.keys[i.currentIndex].Action,
	})
	if err != nil {
		i.err = err
		return false
	}

	i.value, i.err = io.ReadAll(data)
	if i.err != nil {
		return false
	}

	// increment the offset
	i.offset++

	return true
}

func (i *kvListIterator) Error() error {
	return nil
}

func (i *kvListIterator) ContinueToken() string {
	return ContinueToken{
		StartOffset:     i.offset,
		ResourceVersion: i.listRV,
	}.String()
}

func (i *kvListIterator) ResourceVersion() int64 {
	return i.rv
}

func (i *kvListIterator) Namespace() string {
	return i.keys[i.currentIndex].Namespace
}

func (i *kvListIterator) Name() string {
	return i.keys[i.currentIndex].Name
}

func (i *kvListIterator) Folder() string {
	return i.keys[i.currentIndex].Folder
}

func (i *kvListIterator) Value() []byte {
	return i.value
}

// ListHistory is like ListIterator, but it returns the history of a resource.
func (k *kvStorageBackend) ListHistory(ctx context.Context, req *resourcepb.ListRequest, fn func(ListIterator) error) (int64, error) {
	if req.Options == nil || req.Options.Key == nil {
		return 0, fmt.Errorf("missing options or key in ListRequest")
	}
	key := req.Options.Key
	if key.Group == "" {
		return 0, fmt.Errorf("group is required")
	}
	if key.Resource == "" {
		return 0, fmt.Errorf("resource is required")
	}
	if key.Namespace == "" {
		return 0, fmt.Errorf("namespace is required")
	}
	if key.Name == "" {
		return 0, fmt.Errorf("name is required")
	}

	// Parse continue token if provided
	lastSeenRV := int64(0)
	sortAscending := req.GetVersionMatchV2() == resourcepb.ResourceVersionMatchV2_NotOlderThan
	if req.NextPageToken != "" {
		token, err := GetContinueToken(req.NextPageToken)
		if err != nil {
			return 0, fmt.Errorf("invalid continue token: %w", err)
		}
		lastSeenRV = token.ResourceVersion
		sortAscending = token.SortAscending
	}

	// Generate a new resource version for the list
	listRV := k.snowflake.Generate().Int64()

	// Get all history entries by iterating through datastore keys
	historyKeys := make([]DataKey, 0, min(defaultListBufferSize, req.Limit+1))

	// Use datastore.Keys to get all data keys for this specific resource
	for dataKey, err := range k.dataStore.Keys(ctx, ListRequestKey{
		Namespace: key.Namespace,
		Group:     key.Group,
		Resource:  key.Resource,
		Name:      key.Name,
	}) {
		if err != nil {
			return 0, err
		}
		historyKeys = append(historyKeys, dataKey)
	}

	// Handle trash differently from regular history
	if req.Source == resourcepb.ListRequest_TRASH {
		return k.processTrashEntries(ctx, req, fn, historyKeys, lastSeenRV, sortAscending, listRV)
	}

	// Apply filtering based on version match

	switch req.GetVersionMatchV2() {
	case resourcepb.ResourceVersionMatchV2_Exact:
		if req.ResourceVersion <= 0 {
			return 0, fmt.Errorf("expecting an explicit resource version query when using Exact matching")
		}
		var exactKeys []DataKey
		for _, key := range historyKeys {
			if key.ResourceVersion == req.ResourceVersion {
				exactKeys = append(exactKeys, key)
			}
		}
		historyKeys = exactKeys
	case resourcepb.ResourceVersionMatchV2_NotOlderThan:
		if req.ResourceVersion > 0 {
			var filteredKeys []DataKey
			for _, key := range historyKeys {
				if key.ResourceVersion >= req.ResourceVersion {
					filteredKeys = append(filteredKeys, key)
				}
			}
			historyKeys = filteredKeys
		}
	default:
		if req.ResourceVersion > 0 {
			var filteredKeys []DataKey
			for _, key := range historyKeys {
				if key.ResourceVersion <= req.ResourceVersion {
					filteredKeys = append(filteredKeys, key)
				}
			}
			historyKeys = filteredKeys
		}
	}

	// Apply "live" history logic: ignore events before the last delete
	useLatestDeletionAsMinRV := req.ResourceVersion == 0 && req.Source != resourcepb.ListRequest_TRASH && req.GetVersionMatchV2() != resourcepb.ResourceVersionMatchV2_Exact
	if useLatestDeletionAsMinRV {
		latestDeleteRV := int64(0)
		for _, key := range historyKeys {
			if key.Action == DataActionDeleted && key.ResourceVersion > latestDeleteRV {
				latestDeleteRV = key.ResourceVersion
			}
		}
		if latestDeleteRV > 0 {
			var liveKeys []DataKey
			for _, key := range historyKeys {
				if key.ResourceVersion > latestDeleteRV {
					liveKeys = append(liveKeys, key)
				}
			}
			historyKeys = liveKeys
		}
	}

	// Sort the entries if not already sorted correctly
	if sortAscending {
		sort.Slice(historyKeys, func(i, j int) bool {
			return historyKeys[i].ResourceVersion < historyKeys[j].ResourceVersion
		})
	} else {
		sort.Slice(historyKeys, func(i, j int) bool {
			return historyKeys[i].ResourceVersion > historyKeys[j].ResourceVersion
		})
	}

	// Pagination: filter out items up to and including lastSeenRV
	var pagedKeys []DataKey
	for _, key := range historyKeys {
		if lastSeenRV == 0 {
			pagedKeys = append(pagedKeys, key)
		} else if sortAscending && key.ResourceVersion > lastSeenRV {
			pagedKeys = append(pagedKeys, key)
		} else if !sortAscending && key.ResourceVersion < lastSeenRV {
			pagedKeys = append(pagedKeys, key)
		}
	}

	iter := kvHistoryIterator{
		keys:          pagedKeys,
		currentIndex:  -1,
		ctx:           ctx,
		listRV:        listRV,
		sortAscending: sortAscending,
		dataStore:     k.dataStore,
	}

	err := fn(&iter)
	if err != nil {
		return 0, err
	}

	return listRV, nil
}

// processTrashEntries handles the special case of listing deleted items (trash)
func (k *kvStorageBackend) processTrashEntries(ctx context.Context, req *resourcepb.ListRequest, fn func(ListIterator) error, historyKeys []DataKey, lastSeenRV int64, sortAscending bool, listRV int64) (int64, error) {
	// Filter to only deleted entries
	var deletedKeys []DataKey
	for _, key := range historyKeys {
		if key.Action == DataActionDeleted {
			deletedKeys = append(deletedKeys, key)
		}
	}

	// Check if the resource currently exists (is live)
	// If it exists, don't return any trash entries
	_, err := k.metaStore.GetLatestResourceKey(ctx, MetaGetRequestKey{
		Namespace: req.Options.Key.Namespace,
		Group:     req.Options.Key.Group,
		Resource:  req.Options.Key.Resource,
		Name:      req.Options.Key.Name,
	})

	var trashKeys []DataKey
	if err == ErrNotFound {
		// Resource doesn't exist currently, so we can return the latest delete
		// Find the latest delete event
		var latestDelete *DataKey
		for _, key := range deletedKeys {
			if latestDelete == nil || key.ResourceVersion > latestDelete.ResourceVersion {
				latestDelete = &key
			}
		}
		if latestDelete != nil {
			trashKeys = append(trashKeys, *latestDelete)
		}
	}
	// If err != ErrNotFound, the resource exists, so no trash entries should be returned

	// Apply version filtering
	switch req.GetVersionMatchV2() {
	case resourcepb.ResourceVersionMatchV2_Exact:
		if req.ResourceVersion <= 0 {
			return 0, fmt.Errorf("expecting an explicit resource version query when using Exact matching")
		}
		var exactKeys []DataKey
		for _, key := range trashKeys {
			if key.ResourceVersion == req.ResourceVersion {
				exactKeys = append(exactKeys, key)
			}
		}
		trashKeys = exactKeys
	case resourcepb.ResourceVersionMatchV2_NotOlderThan:
		if req.ResourceVersion > 0 {
			var filteredKeys []DataKey
			for _, key := range trashKeys {
				if key.ResourceVersion >= req.ResourceVersion {
					filteredKeys = append(filteredKeys, key)
				}
			}
			trashKeys = filteredKeys
		}
	default:
		if req.ResourceVersion > 0 {
			var filteredKeys []DataKey
			for _, key := range trashKeys {
				if key.ResourceVersion <= req.ResourceVersion {
					filteredKeys = append(filteredKeys, key)
				}
			}
			trashKeys = filteredKeys
		}
	}

	// Sort the entries
	if sortAscending {
		sort.Slice(trashKeys, func(i, j int) bool {
			return trashKeys[i].ResourceVersion < trashKeys[j].ResourceVersion
		})
	} else {
		sort.Slice(trashKeys, func(i, j int) bool {
			return trashKeys[i].ResourceVersion > trashKeys[j].ResourceVersion
		})
	}

	// Pagination: filter out items up to and including lastSeenRV
	var pagedKeys []DataKey
	for _, key := range trashKeys {
		if lastSeenRV == 0 {
			pagedKeys = append(pagedKeys, key)
		} else if sortAscending && key.ResourceVersion > lastSeenRV {
			pagedKeys = append(pagedKeys, key)
		} else if !sortAscending && key.ResourceVersion < lastSeenRV {
			pagedKeys = append(pagedKeys, key)
		}
	}

	iter := kvHistoryIterator{
		keys:          pagedKeys,
		currentIndex:  -1,
		ctx:           ctx,
		listRV:        listRV,
		sortAscending: sortAscending,
		dataStore:     k.dataStore,
	}

	err = fn(&iter)
	if err != nil {
		return 0, err
	}

	return listRV, nil
}

// kvHistoryIterator implements ListIterator for KV storage history
type kvHistoryIterator struct {
	ctx           context.Context
	keys          []DataKey
	currentIndex  int
	listRV        int64
	sortAscending bool
	dataStore     *dataStore

	// current
	rv     int64
	err    error
	value  []byte
	folder string
}

func (i *kvHistoryIterator) Next() bool {
	i.currentIndex++

	if i.currentIndex >= len(i.keys) {
		return false
	}

	key := i.keys[i.currentIndex]
	i.rv = key.ResourceVersion

	// Read the value from the ReadCloser
	data, err := i.dataStore.Get(i.ctx, key)
	if err != nil {
		i.err = err
		return false
	}
	i.value, err = io.ReadAll(data)
	if err != nil {
		i.err = err
		return false
	}

	// Extract the folder from the meta data
	partial := &metav1.PartialObjectMetadata{}
	err = json.Unmarshal(i.value, partial)
	if err != nil {
		i.err = err
		return false
	}

	meta, err := utils.MetaAccessor(partial)
	if err != nil {
		i.err = err
		return false
	}
	i.folder = meta.GetFolder()
	i.err = nil

	return true
}

func (i *kvHistoryIterator) Error() error {
	return i.err
}

func (i *kvHistoryIterator) ContinueToken() string {
	if i.currentIndex < 0 || i.currentIndex >= len(i.keys) {
		return ""
	}
	token := ContinueToken{
		StartOffset:     i.rv,
		ResourceVersion: i.keys[i.currentIndex].ResourceVersion,
		SortAscending:   i.sortAscending,
	}
	return token.String()
}

func (i *kvHistoryIterator) ResourceVersion() int64 {
	return i.rv
}

func (i *kvHistoryIterator) Namespace() string {
	if i.currentIndex >= 0 && i.currentIndex < len(i.keys) {
		return i.keys[i.currentIndex].Namespace
	}
	return ""
}

func (i *kvHistoryIterator) Name() string {
	if i.currentIndex >= 0 && i.currentIndex < len(i.keys) {
		return i.keys[i.currentIndex].Name
	}
	return ""
}

func (i *kvHistoryIterator) Folder() string {
	return i.folder
}

func (i *kvHistoryIterator) Value() []byte {
	return i.value
}

// WatchWriteEvents returns a channel that receives write events.
func (k *kvStorageBackend) WatchWriteEvents(ctx context.Context) (<-chan *WrittenEvent, error) {
	// Create a channel to receive events
	events := make(chan *WrittenEvent, 10000) // TODO: make this configurable

	notifierEvents := k.notifier.Watch(ctx, defaultWatchOptions())
	go func() {
		for event := range notifierEvents {
			// fetch the data
			dataReader, err := k.dataStore.Get(ctx, DataKey{
				Namespace:       event.Namespace,
				Group:           event.Group,
				Resource:        event.Resource,
				Name:            event.Name,
				ResourceVersion: event.ResourceVersion,
				Action:          event.Action,
			})
			if err != nil {
				return
			}
			data, err := io.ReadAll(dataReader)
			if err != nil {
				return
			}
			var t resourcepb.WatchEvent_Type
			switch event.Action {
			case DataActionCreated:
				t = resourcepb.WatchEvent_ADDED
			case DataActionUpdated:
				t = resourcepb.WatchEvent_MODIFIED
			case DataActionDeleted:
				t = resourcepb.WatchEvent_DELETED
			}

			events <- &WrittenEvent{
				Key: &resourcepb.ResourceKey{
					Namespace: event.Namespace,
					Group:     event.Group,
					Resource:  event.Resource,
					Name:      event.Name,
				},
				Type:            t,
				Folder:          event.Folder,
				Value:           data,
				ResourceVersion: event.ResourceVersion,
				PreviousRV:      event.PreviousRV,
				Timestamp:       event.ResourceVersion / time.Second.Nanoseconds(), // convert to seconds
			}
		}
		close(events)
	}()
	return events, nil
}

// GetResourceStats returns resource stats within the storage backend.
// TODO: this isn't very efficient, we should use a more efficient algorithm.
func (k *kvStorageBackend) GetResourceStats(ctx context.Context, namespace string, minCount int) ([]ResourceStats, error) {
	stats := make([]ResourceStats, 0)
	res := make(map[string]map[string]bool)
	rvs := make(map[string]int64)

	// Use datastore.Keys to get all data keys for the namespace
	for dataKey, err := range k.dataStore.Keys(ctx, ListRequestKey{Namespace: namespace}) {
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%s/%s", dataKey.Namespace, dataKey.Group, dataKey.Resource)
		if _, ok := res[key]; !ok {
			res[key] = make(map[string]bool)
			rvs[key] = 1
		}
		res[key][dataKey.Name] = dataKey.Action != DataActionDeleted
		rvs[key] = dataKey.ResourceVersion
	}

	for key, names := range res {
		parts := strings.Split(key, "/")
		count := int64(0)
		for _, exists := range names {
			if exists {
				count++
			}
		}
		if count <= int64(minCount) {
			continue
		}
		stats = append(stats, ResourceStats{
			NamespacedResource: NamespacedResource{
				Namespace: parts[0],
				Group:     parts[1],
				Resource:  parts[2],
			},
			Count:           count,
			ResourceVersion: rvs[key],
		})
	}
	return stats, nil
}
