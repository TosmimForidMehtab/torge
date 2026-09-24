package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Database holds database connection settings that can be given either as a
// single URL or as separate parts. Embed it with an envPrefix:
//
//	type Config struct {
//	    DB config.Database `envPrefix:"DB_"`
//	}
//
// reads DB_URL, or DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME and
// DB_PARAMS. With envPrefix:"DATABASE_" the platform-standard DATABASE_URL
// works too. When URL is set it wins and the parts are ignored.
//
// The DSN methods build driver-specific connection strings with correct
// escaping, so passwords containing '@', '/' or ':' are safe:
//
//	sqldb.Open("pgx", cfg.DB.PostgresDSN())      // or torgepgx.Open(ctx, ...)
//	sqldb.Open("mysql", cfg.DB.MySQLDSN())
//	sqldb.Open("sqlite", cfg.DB.SQLiteDSN())
//	torgemongo.Open(cfg.DB.MongoURI(), cfg.DB.Database())
//
// The returned strings contain the password: never log them. Database itself
// prints safely because URL and Password are Secrets.
type Database struct {
	// URL is a complete connection URL, for example
	// postgres://user:pass@host:5432/app?sslmode=require. For SQLite it may be
	// a file path.
	URL Secret `env:"URL"`
	// Host is the server host name or IP address.
	Host string `env:"HOST"`
	// Port is the server port; 0 uses the driver's default.
	Port int `env:"PORT" validate:"min=0,max=65535"`
	// User and Password authenticate the connection.
	User     string `env:"USER"`
	Password Secret `env:"PASSWORD"`
	// Name is the database name (for SQLite, the file path).
	Name string `env:"NAME"`
	// Params are extra driver options in query-string form, for example
	// "sslmode=require&connect_timeout=5".
	Params string `env:"PARAMS"`
}

// Validate implements validate.Validatable: either URL, or Host (or, for
// SQLite, Name) must be set, and URL and Params must parse.
func (d Database) Validate() error {
	var errs validate.Errors
	switch {
	case d.URL != "":
		if strings.Contains(string(d.URL), "://") {
			if _, err := url.Parse(string(d.URL)); err != nil {
				errs = append(errs, validate.FieldError{Field: "URL", Rule: "url", Message: "is not a valid URL"})
			}
		}
	case d.Host == "" && d.Name == "":
		errs = append(errs, validate.FieldError{Field: "URL", Rule: "required",
			Message: "is required: set the URL, or the host and database name"})
	}
	if _, err := url.ParseQuery(d.Params); err != nil {
		errs = append(errs, validate.FieldError{Field: "Params", Rule: "query", Message: "must be in key=value&key=value form"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Database returns the database name: Name, or the path of URL.
func (d Database) Database() string {
	if d.URL == "" {
		return d.Name
	}
	u, err := url.Parse(string(d.URL))
	if err != nil {
		return d.Name
	}
	return strings.TrimPrefix(u.Path, "/")
}

// PostgresDSN returns a postgres:// URL accepted by pgx, lib/pq and most
// PostgreSQL tools.
func (d Database) PostgresDSN() string {
	if d.URL != "" {
		return string(d.URL)
	}
	return d.buildURL("postgres")
}

// MongoURI returns a mongodb:// URI. Use the URL form for mongodb+srv://
// or multi-host replica set seed lists.
func (d Database) MongoURI() string {
	if d.URL != "" {
		return string(d.URL)
	}
	u := d.urlParts("mongodb")
	u.Path = "/"
	return u.String()
}

// MySQLDSN returns a DSN for github.com/go-sql-driver/mysql
// (user:pass@tcp(host:port)/name?params). A mysql:// URL is converted. If not
// specified otherwise, parseTime=true is added so DATETIME columns scan into
// time.Time.
func (d Database) MySQLDSN() string {
	parts := d
	if d.URL != "" {
		raw := string(d.URL)
		if !strings.HasPrefix(raw, "mysql://") {
			return raw // already in driver DSN format
		}
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		parts = Database{Host: u.Hostname(), User: u.User.Username(), Name: strings.TrimPrefix(u.Path, "/"), Params: u.RawQuery}
		parts.Port, _ = strconv.Atoi(u.Port())
		if p, ok := u.User.Password(); ok {
			parts.Password = Secret(p)
		}
	}
	var b strings.Builder
	if parts.User != "" {
		b.WriteString(parts.User)
		if parts.Password != "" {
			b.WriteByte(':')
			b.WriteString(parts.Password.Value())
		}
		b.WriteByte('@')
	}
	b.WriteString("tcp(")
	b.WriteString(hostPort(parts.Host, parts.Port))
	b.WriteString(")/")
	b.WriteString(parts.Name)
	q, _ := url.ParseQuery(parts.Params)
	if !q.Has("parseTime") {
		q.Set("parseTime", "true")
	}
	b.WriteByte('?')
	b.WriteString(q.Encode())
	return b.String()
}

// SQLiteDSN returns the database file path (or "file:" URI) for SQLite
// drivers, taken from URL or Name, with Params appended. A sqlite:// prefix
// is removed.
func (d Database) SQLiteDSN() string {
	dsn := d.Name
	if d.URL != "" {
		dsn = strings.TrimPrefix(strings.TrimPrefix(string(d.URL), "sqlite3://"), "sqlite://")
	}
	if d.Params != "" {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + d.Params
	}
	return dsn
}

func (d Database) buildURL(scheme string) string {
	u := d.urlParts(scheme)
	u.Path = "/" + d.Name
	return u.String()
}

func (d Database) urlParts(scheme string) *url.URL {
	u := &url.URL{Scheme: scheme, Host: hostPort(d.Host, d.Port), RawQuery: d.Params}
	switch {
	case d.User != "" && d.Password != "":
		u.User = url.UserPassword(d.User, d.Password.Value())
	case d.User != "":
		u.User = url.User(d.User)
	}
	return u
}

func hostPort(host string, port int) string {
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			return "[" + host + "]" // bare IPv6
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
