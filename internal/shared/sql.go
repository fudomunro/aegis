package shared

// RowScanner is the Scan subset of *sql.Rows / *sql.Row, so store packages
// can write one decoder for both.
type RowScanner interface {
	Scan(dest ...any) error
}
