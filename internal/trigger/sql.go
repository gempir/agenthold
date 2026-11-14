package trigger

import "database/sql"

func sqlNull(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
