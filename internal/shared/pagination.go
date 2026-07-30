package shared

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

const (
	defaultPerPage = 25
	maxPerPage     = 200
)

// Query captures the pagination, sorting, filtering and search parameters that
// every list endpoint accepts:
//
//	?page=2&per_page=50&sort=-created_at&q=widget&filter[status]=active
type Query struct {
	Page    int
	PerPage int
	Sort    []SortField
	Search  string
	Filters map[string]string
}

// SortField is a single parsed sort term.
type SortField struct {
	Column string
	Desc   bool
}

// PageMeta is the pagination block returned in the `meta` key of list responses.
type PageMeta struct {
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewPageMeta computes the meta block for a page of results.
func NewPageMeta(q Query, total int64) PageMeta {
	pages := 0
	if q.PerPage > 0 {
		pages = int((total + int64(q.PerPage) - 1) / int64(q.PerPage))
	}
	return PageMeta{Page: q.Page, PerPage: q.PerPage, Total: total, TotalPages: pages}
}

// ParseQuery reads list parameters off the request, clamping them to safe
// bounds. Unparseable values fall back to defaults rather than erroring, so a
// stray `?page=abc` never 400s a dashboard.
func ParseQuery(c *fiber.Ctx) Query {
	q := Query{
		Page:    1,
		PerPage: defaultPerPage,
		Search:  strings.TrimSpace(c.Query("q")),
		Filters: map[string]string{},
	}

	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		q.Page = v
	}
	if v, err := strconv.Atoi(c.Query("per_page")); err == nil && v > 0 {
		q.PerPage = min(v, maxPerPage)
	}

	for _, term := range strings.Split(c.Query("sort"), ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.HasPrefix(term, "-") {
			q.Sort = append(q.Sort, SortField{Column: term[1:], Desc: true})
			continue
		}
		q.Sort = append(q.Sort, SortField{Column: term})
	}

	// filter[status]=active style params.
	c.Request().URI().QueryArgs().VisitAll(func(k, v []byte) {
		key := string(k)
		if strings.HasPrefix(key, "filter[") && strings.HasSuffix(key, "]") {
			name := key[len("filter[") : len(key)-1]
			if name != "" {
				q.Filters[name] = string(v)
			}
		}
	})

	return q
}

// Offset is the SQL OFFSET for the current page.
func (q Query) Offset() int { return (q.Page - 1) * q.PerPage }

// Paginate returns a GORM scope applying LIMIT/OFFSET.
func (q Query) Paginate() func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Limit(q.PerPage).Offset(q.Offset())
	}
}

// OrderBy returns a GORM scope applying ORDER BY, restricted to an allow-list
// of sortable columns. Anything not on the list is ignored, which keeps user
// input out of the SQL string.
func (q Query) OrderBy(allowed map[string]string, fallback string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		applied := false
		for _, s := range q.Sort {
			col, ok := allowed[s.Column]
			if !ok {
				continue
			}
			dir := "ASC"
			if s.Desc {
				dir = "DESC"
			}
			db = db.Order(fmt.Sprintf("%s %s", col, dir))
			applied = true
		}
		if !applied && fallback != "" {
			db = db.Order(fallback)
		}
		return db
	}
}

// FilterEq applies `column = value` for each filter key present in the
// allow-list, mapping the public filter name to a real column.
func (q Query) FilterEq(allowed map[string]string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		for name, val := range q.Filters {
			col, ok := allowed[name]
			if !ok || val == "" {
				continue
			}
			db = db.Where(col+" = ?", val)
		}
		return db
	}
}
