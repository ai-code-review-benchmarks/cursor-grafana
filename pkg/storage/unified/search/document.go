package search

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/grafana/grafana/pkg/apimachinery/utils"
	"github.com/grafana/grafana/pkg/services/store/kind/dashboard"
	"github.com/grafana/grafana/pkg/storage/unified/resource"
)

type DocumentBuilderProvider interface {
	// The list returned here defines the set of resource kinds we know about and how to
	// convert them to documents.  Long term this will likely need to to understand
	// the "app manifest" that would includes declarative way to identify searchable fields
	GetDocumentBuilders(ctx context.Context) ([]resource.DocumentBuilderInfo, error)
}

// Replaced in enterprise with a version that includes stats
func ProvideBuilders() DocumentBuilderProvider {
	return &standardDocumentProvider{}
}

type standardDocumentProvider struct {
	_ int // sealed
}

type defaultDocumentBuilder struct {
	_ int // sealed
}

var (
	_ resource.DocumentBuilder = &defaultDocumentBuilder{}
	_ DocumentBuilderProvider  = &standardDocumentProvider{}
)

func (p *standardDocumentProvider) GetDocumentBuilders(ctx context.Context) ([]resource.DocumentBuilderInfo, error) {
	return []resource.DocumentBuilderInfo{
		{
			Builder: &defaultDocumentBuilder{},
		},
		{
			GroupResource: schema.GroupResource{
				Group:    "dashboard.grafana.app",
				Resource: "dashboards",
			},

			// This is a dummy example, and will need resolver setup for enterprise stats and and (eventually) data sources
			Namespaced: func(ctx context.Context, namespace string, blob resource.BlobSupport) (resource.DocumentBuilder, error) {
				lookup := dashboard.CreateDatasourceLookup([]*dashboard.DatasourceQueryResult{
					// TODO, query data sources
				})
				return &DashboardDocumentBuilder{
					Namespace:        namespace,
					DatasourceLookup: lookup,
					Stats:            nil, // loaded in enterprise
					Blob:             blob,
				}, nil
			},
		},
	}, nil
}

func (*defaultDocumentBuilder) BuildDocument(_ context.Context, key *resource.ResourceKey, rv int64, value []byte) (resource.IndexableDocument, error) {
	tmp := &unstructured.Unstructured{}
	err := tmp.UnmarshalJSON(value)
	if err != nil {
		return nil, err
	}

	obj, err := utils.MetaAccessor(tmp)
	if err != nil {
		return nil, err
	}

	doc := &StandardDocumentFields{}
	doc.Load(key, rv, obj)

	doc.Title = obj.FindTitle(doc.Name)
	doc.ByteSize = len(value)

	return doc, nil
}

// This is common across all resources
type StandardDocumentFields struct {
	// unique ID across everything (group+resource+namespace+name)
	ID        string `json:"id"`
	RV        int64  `json:"rv"`
	Kind      string
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`

	Folder      string   `json:"folder,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	ByteSize    int      `json:"byte_size,omitempty"`

	// Standard k8s style labels
	Labels map[string]string `json:"labels,omitempty"`

	Created   time.Time  `json:"created,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	Updated   *time.Time `json:"updated,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`

	OriginName string `json:"origin_name,omitempty"`
	OriginPath string `json:"origin_path,omitempty"`
	OriginHash string `json:"origin_hash,omitempty"`
	OriginTime int64  `json:"origin_time,omitempty"`
}

func (s *StandardDocumentFields) GetID() string {
	return s.ID
}

// Load values from standard object
func (s *StandardDocumentFields) Load(key *resource.ResourceKey, rv int64, obj utils.GrafanaMetaAccessor) {
	s.ID = toID(key)
	s.RV = rv
	s.Labels = obj.GetLabels()

	s.Group = key.Group
	s.Resource = key.Resource
	s.Namespace = key.Namespace
	s.Name = key.Name

	s.Folder = obj.GetFolder()
	s.Created = obj.GetCreationTimestamp().Time.UTC()
	s.CreatedBy = obj.GetCreatedBy()

	ts, _ := obj.GetUpdatedTimestamp()
	if ts != nil {
		utc := ts.UTC()
		s.Updated = &utc
	}
	s.UpdatedBy = obj.GetUpdatedBy()

	origin, _ := obj.GetOriginInfo()
	if origin != nil {
		s.OriginName = origin.Name
		s.OriginPath = origin.Path
		s.OriginHash = origin.Hash
		if origin.Timestamp != nil {
			s.OriginTime = origin.Timestamp.UnixMilli()
		}
	}
}

func toID(key *resource.ResourceKey) string {
	ns := key.Namespace
	if ns == "" {
		ns = "*cluster*"
	}
	var sb strings.Builder
	sb.WriteString(key.Group)
	sb.WriteString("/")
	sb.WriteString(key.Resource)
	sb.WriteString("/")
	sb.WriteString(ns)
	sb.WriteString("/")
	sb.WriteString(key.Name)
	return sb.String()
}
