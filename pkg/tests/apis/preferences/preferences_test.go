package preferences

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	preferences "github.com/grafana/grafana/apps/preferences/pkg/apis/preferences/v1alpha1"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/tests/apis"
	"github.com/grafana/grafana/pkg/tests/testinfra"
	"github.com/grafana/grafana/pkg/tests/testsuite"
)

func TestMain(m *testing.M) {
	testsuite.Run(m)
}

func TestIntegrationPreferences(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	helper := apis.NewK8sTestHelper(t, testinfra.GrafanaOpts{
		AppModeProduction: false, // required for experimental APIs
		DisableAnonymous:  true,
		EnableFeatureToggles: []string{
			featuremgmt.FlagGrafanaAPIServerWithExperimentalAPIs,
		},
	})

	t.Run("preferences", func(t *testing.T) {
		ctx := context.Background()
		clientAdmin := helper.GetResourceClient(apis.ResourceClientArgs{
			User: helper.Org1.Admin,
			GVR:  preferences.PreferencesResourceInfo.GroupVersionResource(),
		})
		clientViewer := helper.GetResourceClient(apis.ResourceClientArgs{
			User: helper.Org1.Viewer,
			GVR:  preferences.PreferencesResourceInfo.GroupVersionResource(),
		})

		// List is empty when we start
		rsp, err := clientAdmin.Resource.List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Empty(t, rsp.Items, "no preferences saved yet")

		raw := make(map[string]any)
		legacyResponse := apis.DoRequest(helper, apis.RequestParams{
			User:   clientAdmin.Args.User,
			Method: http.MethodPut,
			Path:   "/api/user/preferences",
			Body: []byte(`{
				"theme": "dark",
				"timezone": "Africa/Addis_Ababa",
				"weekStart": "saturday",
				"language": "it-IT"
			}`),
		}, &raw)
		require.Equal(t, http.StatusOK, legacyResponse.Response.StatusCode, "create preference for user")

		// http://localhost:3000/api/teams/1/preferences
		legacyResponse = apis.DoRequest(helper, apis.RequestParams{
			User:   clientAdmin.Args.User,
			Method: http.MethodPut,
			Path:   fmt.Sprintf("/api/teams/%d/preferences", helper.Org1.Staff.ID),
			Body: []byte(`{
				"theme": "light",
				"weekStart": "sunday"
			}`),
		}, &raw)
		require.Equal(t, http.StatusOK, legacyResponse.Response.StatusCode, "create preference for user")

		// http://localhost:3000/api/org/preferences
		legacyResponse = apis.DoRequest(helper, apis.RequestParams{
			User:   clientAdmin.Args.User,
			Method: http.MethodPut,
			Path:   "/api/org/preferences",
			Body: []byte(`{
				"theme": "light",
				"weekStart": "monday"
			}`),
		}, &raw)
		require.Equal(t, http.StatusOK, legacyResponse.Response.StatusCode, "create preference for user")

		// Admin has access to all three (namespace, team, and user)
		rsp, err = clientAdmin.Resource.List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		names := []string{}
		for _, item := range rsp.Items {
			names = append(names, item.GetName())
		}
		require.Equal(t, []string{
			"namespace",
			fmt.Sprintf("team:%s", helper.Org1.Staff.UID),
			clientAdmin.Args.User.Identity.GetUID(),
		}, names)

		// The viewer should only have namespace (eg org level) permissions
		rsp, err = clientViewer.Resource.List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		names = []string{}
		for _, item := range rsp.Items {
			names = append(names, item.GetName())
		}
		require.Equal(t, []string{"namespace"}, names)
	})
}
