package httpclientprovider

import (
	"errors"
	"net/http"

	sdkhttpclient "github.com/grafana/grafana-plugin-sdk-go/backend/httpclient"

	"github.com/grafana/grafana/pkg/services/datasources"
	"github.com/grafana/grafana/pkg/services/validations"
)

const HostRedirectValidationMiddlewareName = "host-redirect-validation"

func RedirectLimitMiddleware(reqValidator validations.DataSourceRequestValidator) sdkhttpclient.Middleware {
	return sdkhttpclient.NamedMiddlewareFunc(HostRedirectValidationMiddlewareName, func(opts sdkhttpclient.Options, next http.RoundTripper) http.RoundTripper {
		return sdkhttpclient.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			res, err := next.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			if res.StatusCode >= 300 && res.StatusCode < 400 {
				location, locationErr := res.Location()
				if errors.Is(locationErr, http.ErrNoLocation) {
					return res, nil
				}
				if locationErr != nil {
					return nil, locationErr
				}

				if validationErr := reqValidator.Validate(&datasources.DataSource{URL: location.String()}, nil); validationErr != nil {
					return nil, validationErr
				}
			}
			return res, nil
		})
	})
}
