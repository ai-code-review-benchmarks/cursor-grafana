package legacy

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"

	authlib "github.com/grafana/authlib/types"
	dashboardsV1 "github.com/grafana/grafana/apps/dashboard/pkg/apis/dashboard/v1beta1"
	preferences "github.com/grafana/grafana/apps/preferences/pkg/apis/preferences/v1alpha1"
	"github.com/grafana/grafana/pkg/apimachinery/utils"
	"github.com/grafana/grafana/pkg/services/apiserver/endpoints/request"
	"github.com/grafana/grafana/pkg/storage/legacysql"
)

var (
	_ rest.Scoper               = (*starsStorage)(nil)
	_ rest.SingularNameProvider = (*starsStorage)(nil)
	_ rest.Getter               = (*starsStorage)(nil)
	_ rest.Lister               = (*starsStorage)(nil)
	_ rest.Storage              = (*starsStorage)(nil)
	// _ rest.Creater              = (*starsStorage)(nil)
	// _ rest.Updater              = (*starsStorage)(nil)
	// _ rest.GracefulDeleter      = (*starsStorage)(nil)
)

func NewStarsStorage(namespacer request.NamespaceMapper, db legacysql.LegacyDatabaseProvider) *starsStorage {
	return &starsStorage{
		namespacer: namespacer,
		sql:        &legacyStarSQL{db: db},
		tableConverter: utils.NewTableConverter(
			schema.GroupResource{
				Group:    preferences.APIGroup,
				Resource: preferences.StarsKind().Plural(),
			},
			utils.TableColumns{
				Definition: []metav1.TableColumnDefinition{
					{Name: "Name", Type: "string", Format: "name"},
					{Name: "Title", Type: "string", Format: "string", Description: "The preferences name"},
					{Name: "Created At", Type: "date"},
				},
				Reader: func(obj any) ([]any, error) {
					m, ok := obj.(*preferences.Stars)
					if !ok {
						return nil, fmt.Errorf("expected preferences")
					}
					return []any{
						m.Name,
						"???",
						m.CreationTimestamp.UTC().Format(time.RFC3339),
					}, nil
				},
			}),
	}
}

type starsStorage struct {
	namespacer     request.NamespaceMapper
	tableConverter rest.TableConvertor
	sql            *legacyStarSQL
}

func (s *starsStorage) New() runtime.Object {
	return preferences.StarsKind().ZeroValue()
}

func (s *starsStorage) Destroy() {}

func (s *starsStorage) NamespaceScoped() bool {
	return true // namespace == org
}

func (s *starsStorage) GetSingularName() string {
	return strings.ToLower(preferences.StarsKind().Kind())
}

func (s *starsStorage) NewList() runtime.Object {
	return preferences.StarsKind().ZeroListValue()
}

func (s *starsStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return s.tableConverter.ConvertToTable(ctx, object, tableOptions)
}

func (s *starsStorage) List(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
	ns, err := request.NamespaceInfoFrom(ctx, false)
	if err != nil {
		return nil, err
	}

	if ns.Value == "" {
		// TODO -- make sure the user can list across *all* namespaces
		return nil, fmt.Errorf("TODO... get stars for all orgs")
	}

	list := &preferences.StarsList{}
	found, rv, err := s.sql.GetStars(ctx, ns.OrgID, "")
	if err != nil {
		return nil, err
	}
	for _, v := range found {
		list.Items = append(list.Items, asResource(s.namespacer(v.OrgID), &v))
	}
	if rv > 0 {
		list.ResourceVersion = strconv.FormatInt(rv, 10)
	}
	return list, nil
}

func (s *starsStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	info, err := request.NamespaceInfoFrom(ctx, true)
	if err != nil {
		return nil, err
	}

	ut, uid, err := authlib.ParseTypeID(name)
	if err != nil {
		return nil, fmt.Errorf("invalid name %w", err)
	}
	if ut != authlib.TypeUser {
		return nil, fmt.Errorf("expecting name with prefix: %s", authlib.TypeUser)
	}

	found, _, err := s.sql.GetStars(ctx, info.OrgID, uid)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	obj := asResource(info.Value, &found[0])
	return &obj, nil
}

// func (s *starsStorage) Create(ctx context.Context,
// 	obj runtime.Object,
// 	createValidation rest.ValidateObjectFunc,
// 	options *metav1.CreateOptions,
// ) (runtime.Object, error) {
// 	info, err := request.NamespaceInfoFrom(ctx, true)
// 	if err != nil {
// 		return nil, err
// 	}

// 	stars, ok := obj.(*preferences.Stars)
// 	if !ok {
// 		return nil, fmt.Errorf("expected stars")
// 	}

// 	fmt.Printf("CREATE: %+v // %+v\n", stars, info)

// 	return nil, fmt.Errorf("TODO...")
// }

// func (s *starsStorage) Update(ctx context.Context,
// 	name string,
// 	objInfo rest.UpdatedObjectInfo,
// 	createValidation rest.ValidateObjectFunc,
// 	updateValidation rest.ValidateObjectUpdateFunc,
// 	forceAllowCreate bool,
// 	options *metav1.UpdateOptions,
// ) (runtime.Object, bool, error) {
// 	info, err := request.NamespaceInfoFrom(ctx, true)
// 	if err != nil {
// 		return nil, false, err
// 	}

// 	old, err := s.Get(ctx, name, nil)
// 	if err != nil {
// 		return nil, false, err
// 	}

// 	obj, err := objInfo.UpdatedObject(ctx, old)
// 	if err != nil {
// 		return nil, false, err
// 	}

// 	stars, ok := obj.(*preferences.Stars)
// 	if !ok {
// 		return nil, false, fmt.Errorf("expected stars")
// 	}

// 	fmt.Printf("UPDATE: %+v // %+v\n", stars, info)

// 	return nil, false, fmt.Errorf("TODO...")
// }

func asResource(ns string, v *dashboardStars) preferences.Stars {
	return preferences.Stars{
		ObjectMeta: metav1.ObjectMeta{
			Name:              fmt.Sprintf("user:%s", v.UserUID),
			Namespace:         ns,
			ResourceVersion:   strconv.FormatInt(v.Last, 10),
			CreationTimestamp: metav1.NewTime(time.UnixMilli(v.First)),
		},
		Spec: preferences.StarsSpec{
			Resource: []preferences.StarsResource{{
				Group: dashboardsV1.APIGroup,
				Kind:  "Dashboard",
				Names: v.Dashboards,
			}},
		},
	}
}
