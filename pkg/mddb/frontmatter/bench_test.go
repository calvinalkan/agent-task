package frontmatter_test

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

func BenchmarkFrontmatter_ParseBytesAndLookup(b *testing.B) {
	payload := []byte(wrapFrontmatter(
		"id: 018f5f25-7e7d-7f0a-8c5c-123456789abc\n"+
			"schema_version: 1\n"+
			"title: Example\n"+
			"status: open\n"+
			"priority: 2\n"+
			"blocked-by:\n"+
			"  - abc\n"+
			"  - def\n"+
			"tags: [ops, \"on-call\"]",
		"# Title\nBody\n",
	))

	keyID := []byte("id")
	keySchema := []byte("schema_version")
	keyTitle := []byte("title")

	b.Run("baseline", func(b *testing.B) {
		var sum int

		b.ReportAllocs()
		b.SetBytes(int64(len(payload)))

		for b.Loop() {
			fm, _, err := frontmatter.ParseBytes(payload)
			if err != nil {
				b.Fatal(err)
			}

			id, _ := fm.GetBytes(keyID)
			title, _ := fm.GetBytes(keyTitle)
			_, _ = fm.GetInt(keySchema)

			sum += len(id) + len(title)
		}

		if sum == 0 {
			b.Fatal("sum should not be zero")
		}
	})
}
