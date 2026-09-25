module github.com/TosmimForidMehtab/torge/contrib/tests

go 1.26.0

require (
	github.com/TosmimForidMehtab/torge v0.2.0
	github.com/TosmimForidMehtab/torge/contrib/otel v0.0.0-00010101000000-000000000000
	github.com/TosmimForidMehtab/torge/contrib/redis v0.0.0-00010101000000-000000000000
	github.com/alicebob/miniredis/v2 v2.39.0
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.11.0
	github.com/redis/go-redis/v9 v9.22.0
	go.mongodb.org/mongo-driver/v2 v2.9.1
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/sdk/metric v1.46.0
	modernc.org/sqlite v1.59.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.2.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace (
	github.com/TosmimForidMehtab/torge => ../..
	github.com/TosmimForidMehtab/torge/contrib/otel => ../otel
	github.com/TosmimForidMehtab/torge/contrib/redis => ../redis
)
