// Package filterutil contains representation-neutral retriever filter helpers.
package filterutil

// MaxNegativeFilterValues is the conservative maximum number of IDs in one
// engine-level negative clause. It is intentionally far below Elasticsearch's
// default 65,536 terms limit and SQLite's historical 999 host-parameter limit:
// https://www.elastic.co/guide/en/elasticsearch/reference/current/query-dsl-terms-query.html
// https://www.sqlite.org/limits.html#max_variable_number
const MaxNegativeFilterValues = 256

// ChunkStrings returns independent, ordered chunks without dropping values.
func ChunkStrings(values []string) [][]string {
	if len(values) == 0 {
		return nil
	}

	chunks := make([][]string, 0, (len(values)+MaxNegativeFilterValues-1)/MaxNegativeFilterValues)
	for start := 0; start < len(values); start += MaxNegativeFilterValues {
		end := min(start+MaxNegativeFilterValues, len(values))
		chunk := append([]string(nil), values[start:end]...)
		chunks = append(chunks, chunk)
	}
	return chunks
}
