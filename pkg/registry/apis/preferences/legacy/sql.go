package legacy

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	authlib "github.com/grafana/authlib/types"
	preferences "github.com/grafana/grafana/apps/preferences/pkg/apis/preferences/v1alpha1"
	pref "github.com/grafana/grafana/pkg/services/preference"
	"github.com/grafana/grafana/pkg/storage/legacysql"
	"github.com/grafana/grafana/pkg/storage/unified/sql/sqltemplate"
)

type dashboardStars struct {
	OrgID   int64
	UserUID string
	First   int64
	Last    int64

	Dashboards []string
}

type preferenceModel struct {
	ID               int64
	OrgID            int64
	UserUID          sql.NullString
	TeamUID          sql.NullString
	JSONData         *pref.PreferenceJSONData
	HomeDashboardUID sql.NullString
	Timezone         sql.NullString
	Theme            sql.NullString
	WeekStart        sql.NullString
	Created          time.Time
	Updated          time.Time
}

type LegacySQL struct {
	db legacysql.LegacyDatabaseProvider
}

func NewLegacySQL(db legacysql.LegacyDatabaseProvider) *LegacySQL {
	return &LegacySQL{db: db}
}

// NOTE: this does not support paging -- lets check if that will be a problem in cloud
func (s *LegacySQL) GetStars(ctx context.Context, orgId int64, user string) ([]dashboardStars, int64, error) {
	sql, err := s.db(ctx)
	if err != nil {
		return nil, 0, err
	}

	req := newStarQueryReq(sql, user, orgId)

	q, err := sqltemplate.Execute(sqlStarsQuery, req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute template %q: %w", sqlStarsQuery.Name(), err)
	}

	sess := sql.DB.GetSqlxSession()
	rows, err := sess.Query(ctx, q, req.GetArgs()...)
	defer func() {
		if rows != nil {
			_ = rows.Close()
		}
	}()

	stars := []dashboardStars{}
	current := &dashboardStars{}
	var orgID int64
	var userUID string
	var dashboardUID string
	var updated time.Time

	for rows.Next() {
		err := rows.Scan(&orgID, &userUID, &dashboardUID, &updated)
		if err != nil {
			return nil, 0, err
		}

		if orgID != current.OrgID || userUID != current.UserUID {
			if current.UserUID != "" {
				stars = append(stars, *current)
			}
			current = &dashboardStars{
				OrgID:   orgID,
				UserUID: userUID,
			}
		}
		ts := updated.UnixMilli()
		if ts > current.Last {
			current.Last = ts
		}
		if ts < current.First || current.First == 0 {
			current.First = ts
		}
		current.Dashboards = append(current.Dashboards, dashboardUID)
	}

	// Add the last value
	if current.UserUID != "" {
		stars = append(stars, *current)
	}

	// Find the RV unless it is a user query
	if userUID == "" {
		req.Reset()
		q, err = sqltemplate.Execute(sqlStarsRV, req)
		if err != nil {
			return nil, 0, fmt.Errorf("execute template %q: %w", sqlStarsRV.Name(), err)
		}
		err = sess.Get(ctx, &updated, q)
	}

	return stars, updated.UnixMilli(), err
}

// List all defined preferences in an org (valid for admin users only)
func (s *LegacySQL) listPreferences(ctx context.Context,
	ns string, orgId int64,
	cb func(req *preferencesQuery) (bool, error),
) ([]preferences.Preferences, int64, error) {
	var results []preferences.Preferences
	var rv sql.NullTime

	sql, err := s.db(ctx)
	if err != nil {
		return nil, 0, err
	}

	req := newPreferencesQueryReq(sql, orgId)
	needsRV, err := cb(&req)
	if err != nil {
		return nil, 0, err
	}

	q, err := sqltemplate.Execute(sqlPreferencesQuery, req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute template %q: %w", sqlPreferencesQuery.Name(), err)
	}

	sess := sql.DB.GetSqlxSession()
	rows, err := sess.Query(ctx, q, req.GetArgs()...)
	defer func() {
		if rows != nil {
			_ = rows.Close()
		}
	}()

	for rows.Next() {
		// SELECT p.id, p.org_id,
		//   p.json_data,
		//   p.timezone,
		//   p.theme,
		//   p.week_start,
		//   p.home_dashboard_uid,
		//   u.uid as user_uid,
		//   t.uid as team_uid,
		//   p.created, p.updated

		pref := preferenceModel{}
		err := rows.Scan(&pref.ID, &pref.OrgID,
			&pref.JSONData,
			&pref.Timezone,
			&pref.Theme,
			&pref.WeekStart,
			&pref.HomeDashboardUID,
			&pref.UserUID, &pref.TeamUID,
			&pref.Created, &pref.Updated)
		if err != nil {
			return nil, 0, err
		}
		if pref.Updated.After(rv.Time) {
			rv.Time = pref.Updated
		}

		results = append(results, asPreferencesResource(ns, &pref))
	}

	if needsRV {
		// req.Reset()
		// q, err = sqltemplate.Execute(sqlPreferencesRV, req)
		// if err != nil {
		// 	return nil, 0, fmt.Errorf("execute template %q: %w", sqlPreferencesRV.Name(), err)
		// }
		// err = sess.Get(ctx, &rv, q)
		// if err != nil {
		// 	return nil, 0, fmt.Errorf("unable to get RV %w", err)
		// }
	}
	return results, rv.Time.UnixMilli(), err
}

func (s *LegacySQL) ListPreferences(ctx context.Context, ns string, user string, needsRV bool) (*preferences.PreferencesList, error) {
	info, err := authlib.ParseNamespace(ns)
	if err != nil {
		return nil, err
	}

	found, rv, err := s.listPreferences(ctx, ns, info.OrgID, func(req *preferencesQuery) (bool, error) {
		if req.UserUID != "" {
			req.UserUID = user
			req.UserTeams, err = s.GetTeams(ctx, info.OrgID, user, false)
		}
		return needsRV, err
	})
	if err != nil {
		return nil, err
	}
	list := &preferences.PreferencesList{
		Items: found,
	}
	if rv > 0 {
		list.ResourceVersion = strconv.FormatInt(rv, 10)
	}
	return list, nil
}

func (s *LegacySQL) GetTeams(ctx context.Context, orgId int64, user string, admin bool) ([]string, error) {
	sql, err := s.db(ctx)
	if err != nil {
		return nil, err
	}

	var teams []string
	req := newTeamsQueryReq(sql, orgId, user, admin)

	q, err := sqltemplate.Execute(sqlTeams, req)
	if err != nil {
		return nil, fmt.Errorf("execute template %q: %w", sqlTeams.Name(), err)
	}
	sess := sql.DB.GetSqlxSession()
	err = sess.Select(ctx, &teams, q)
	return teams, err
}
