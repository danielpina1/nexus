package vault

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentOperations runs writes, renames, deletes, queries and checks
// in parallel; the race detector and the final integrity check must be clean.
func TestConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	v := newVault(t, Options{StateDir: t.TempDir(), KeepConflicts: true})
	for i := 0; i < 10; i++ {
		v.Write(ctx, fmt.Sprintf("n/%d.md", i), fmt.Sprintf("# N%d\nlinks [[%d]]\n- [ ] task %d\n", i, (i+1)%10, i), "")
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				p := fmt.Sprintf("n/%d.md", (w*3+i)%10)
				switch i % 5 {
				case 0:
					v.Update(ctx, p, "", func(old []byte) ([]byte, error) { return append(old, []byte("x\n")...), nil })
				case 1:
					v.Query(ctx, "links:[[3]] sort:path", 0)
				case 2:
					if n, err := v.Read(ctx, p); err == nil {
						v.WriteNoteEx(ctx, p, []byte(n.Body+"y\n"), "", n.Commit)
					}
				case 3:
					v.ListTasks(ctx, TaskFilter{})
					v.Related(ctx, p, 3)
				case 4:
					v.Check(ctx, false)
				}
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		v.Rename(ctx, "n/5.md", "m/five.md", false, "")
		v.Delete(ctx, "n/7.md", "", "")
		v.Merge(ctx, "n/8.md", "n/9.md", "")
	}()
	wg.Wait()
	rep, err := v.Check(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range rep.Issues {
		if i.Severity == "error" || i.Kind == IssueIndexStale || i.Kind == IssueIndexMissing || i.Kind == IssueIndexOrphan || i.Kind == IssueTempFile {
			t.Errorf("issue after concurrent ops: %+v", i)
		}
	}
	if len(v.pendingJournals()) != 0 {
		t.Fatal("journal left behind")
	}
}
