package logs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

const (
	attempt  model.AttemptID = 7
	position int             = 2
)

func newDir(t *testing.T) (*Dir, string) {
	t.Helper()
	logDir := t.TempDir()
	return New(logDir, "3f1c0b2e-database"), filepath.Join(logDir, "3f1c0b2e-database", "7", "2.log")
}

func create(t *testing.T, d *Dir) {
	t.Helper()
	if err := d.Create(attempt, position); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func appendAt(t *testing.T, d *Dir, offset int64, data string) int64 {
	t.Helper()
	held, err := d.Append(attempt, position, offset, []byte(data))
	if err != nil {
		t.Fatalf("Append at %d: %v", offset, err)
	}
	return held
}

func content(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(b)
}

func size(t *testing.T, d *Dir) int64 {
	t.Helper()
	n, err := d.Size(attempt, position)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	return n
}

func TestAppendAtZeroIsHeldAndReadsBack(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	if got := appendAt(t, d, 0, "0123456789"); got != 10 {
		t.Errorf("Append = %d, want 10", got)
	}
	if got := size(t, d); got != 10 {
		t.Errorf("Size = %d, want 10", got)
	}
	got, err := d.Read(attempt, position, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "0123456789" {
		t.Errorf("Read = %q, want %q", got, "0123456789")
	}
	if got := content(t, path); got != "0123456789" {
		t.Errorf("file holds %q, want %q", got, "0123456789")
	}
}

func TestAResentChunkIsNotWrittenTwice(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	appendAt(t, d, 0, "0123456789")
	if got := appendAt(t, d, 0, "0123456789"); got != 10 {
		t.Errorf("Append = %d, want 10", got)
	}
	if got := content(t, path); got != "0123456789" {
		t.Errorf("file holds %q, want %q", got, "0123456789")
	}
}

func TestAChunkPartlyHeldWritesOnlyItsNewBytes(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	appendAt(t, d, 0, "0123456789")
	if got := appendAt(t, d, 5, "56789abcde"); got != 15 {
		t.Errorf("Append = %d, want 15", got)
	}
	if got := content(t, path); got != "0123456789abcde" {
		t.Errorf("file holds %q, want %q", got, "0123456789abcde")
	}
}

func TestAChunkPastTheSizeIsRefused(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	appendAt(t, d, 0, "0123456789abcde")
	if _, err := d.Append(attempt, position, 20, []byte("klmno")); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Append at 20 = %v, want model.ErrInvalid", err)
	}
	if got := content(t, path); got != "0123456789abcde" {
		t.Errorf("file holds %q, want %q", got, "0123456789abcde")
	}
}

func TestAppendWithoutCreateIsRefused(t *testing.T) {
	d, path := newDir(t)
	if _, err := d.Append(attempt, position, 0, []byte("0123456789")); !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Append = %v, want model.ErrInvalid", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) = %v, want os.ErrNotExist", path, err)
	}
}

func TestCreateKeepsTheBytesHeld(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	appendAt(t, d, 0, "0123456789")
	create(t, d)
	if got := content(t, path); got != "0123456789" {
		t.Errorf("file holds %q, want %q", got, "0123456789")
	}
}

func TestCreateMakesThePrivateLayout(t *testing.T) {
	d, path := newDir(t)
	create(t, d)
	for p, want := range map[string]os.FileMode{
		filepath.Dir(filepath.Dir(path)): 0o700,
		filepath.Dir(path):               0o700,
		path:                             0o600,
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %v, want %v", p, got, want)
		}
	}
}

func TestTheSameChunkAppendedAtOnceIsHeldOnce(t *testing.T) {
	// Without the per-file lock two plain goroutines rarely meet between
	// the Stat and the Write, so each attempt starts eight appends at once
	// and fifty attempts run, enough to fail in one run without the lock.
	d, _ := newDir(t)
	const chunk = "0123456789"
	for a := range model.AttemptID(50) {
		if err := d.Create(a, position); err != nil {
			t.Fatalf("Create: %v", err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		held := make([]int64, 8)
		errs := make([]error, 8)
		for i := range held {
			wg.Go(func() {
				<-start
				held[i], errs[i] = d.Append(a, position, 0, []byte(chunk))
			})
		}
		close(start)
		wg.Wait()
		for i := range held {
			if errs[i] != nil || held[i] != 10 {
				t.Errorf("attempt %d: Append %d = %d, %v, want 10, nil", a, i, held[i], errs[i])
			}
		}
		got, err := d.Read(a, position, 0, 100)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != chunk {
			t.Errorf("attempt %d: Read = %q, want %q", a, got, chunk)
		}
	}
}

func TestReadFromAnOffset(t *testing.T) {
	d, _ := newDir(t)
	create(t, d)
	appendAt(t, d, 0, "0123456789")
	tests := []struct {
		name   string
		offset int64
		limit  int
		want   string
	}{
		{"up to the limit", 2, 3, "234"},
		{"up to the end", 8, 100, "89"},
		{"at the end", 10, 100, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Read(attempt, position, tc.offset, tc.limit)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Errorf("Read(%d, %d) = %q, want %q", tc.offset, tc.limit, got, tc.want)
			}
		})
	}
}

func TestAStepWithNoFileHoldsNothing(t *testing.T) {
	d, _ := newDir(t)
	if got := size(t, d); got != 0 {
		t.Errorf("Size = %d, want 0", got)
	}
	got, err := d.Read(attempt, position, 0, 100)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Read = %q, want nothing", got)
	}
}
