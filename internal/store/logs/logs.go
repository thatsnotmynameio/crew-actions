// Package logs keeps each step's output on the engine's disk, in one file
// per step and attempt: <log dir>/<database id>/<attempt id>/<position>.log.
//
// The database id roots the directory, so a database made again never
// reads or appends to an old build's files. Paths are built from the
// integer ids alone, so nothing a runner sends names a path.
//
// A log is appended to by offset, so a chunk sent again is never written
// twice: the bytes the file already holds are acknowledged and dropped,
// the bytes past them are written, and a chunk that starts past them is
// refused. Every answer is the number of bytes the file holds.
package logs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// The modes of the directories and files a Dir creates: only the engine's
// user reads a step's output.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// Dir holds the logs of one database's steps.
type Dir struct {
	root string

	mu    sync.Mutex
	files map[step]*sync.Mutex
}

// step names one step of one attempt.
type step struct {
	attempt  model.AttemptID
	position int
}

// New returns the Dir rooted at logDir joined with databaseID. It creates
// nothing until a log is created.
func New(logDir, databaseID string) *Dir {
	return &Dir{root: filepath.Join(logDir, databaseID), files: map[step]*sync.Mutex{}}
}

// Create creates the log of the step at position of the attempt if it is
// absent, and leaves the bytes of one that exists in place.
func (d *Dir) Create(attempt model.AttemptID, position int) error {
	if err := os.MkdirAll(d.dir(attempt), dirMode); err != nil {
		return fmt.Errorf("create the log directory of attempt %d: %w", attempt, err)
	}
	f, err := d.open(attempt, position, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create the log of step %d of attempt %d: %w", position, attempt, err)
	}
	return nil
}

// Append writes the bytes of data, which starts at offset of the log of
// the step at position of the attempt, that the log does not hold yet,
// and returns how many bytes it holds then. A log that does not exist, or
// an offset past the bytes it holds, gives model.ErrInvalid and writes
// nothing.
func (d *Dir) Append(attempt model.AttemptID, position int, offset int64, data []byte) (int64, error) {
	lock := d.lock(attempt, position)
	lock.Lock()
	defer lock.Unlock()

	f, err := d.open(attempt, position, os.O_WRONLY|os.O_APPEND)
	if errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("append to the log of step %d of attempt %d: %w",
			position, attempt, model.Invalid("the step has no log"))
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("read the size of the log of step %d of attempt %d: %w", position, attempt, err)
	}
	size := info.Size()
	if offset > size {
		return 0, fmt.Errorf("append to the log of step %d of attempt %d: %w", position, attempt,
			model.Invalid(fmt.Sprintf("offset %d is past the %d bytes the log holds", offset, size)))
	}
	end := offset + int64(len(data))
	if end <= size {
		return size, nil
	}
	if _, err := f.Write(data[size-offset:]); err != nil {
		return 0, fmt.Errorf("append to the log of step %d of attempt %d: %w", position, attempt, err)
	}
	return end, nil
}

// Size returns how many bytes the log of the step at position of the
// attempt holds, 0 when it does not exist.
func (d *Dir) Size(attempt model.AttemptID, position int) (int64, error) {
	info, err := os.Stat(d.path(attempt, position))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the size of the log of step %d of attempt %d: %w", position, attempt, err)
	}
	return info.Size(), nil
}

// Read returns at most limit bytes of the log of the step at position of
// the attempt, from offset on: none at its end, or when it does not exist.
func (d *Dir) Read(attempt model.AttemptID, position int, offset int64, limit int) ([]byte, error) {
	f, err := d.open(attempt, position, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read the log of step %d of attempt %d: %w", position, attempt, err)
	}
	return buf[:n], nil
}

// lock returns the lock of the log of the step at position of the attempt,
// which serializes its appends.
func (d *Dir) lock(attempt model.AttemptID, position int) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := step{attempt: attempt, position: position}
	lock, ok := d.files[key]
	if !ok {
		lock = new(sync.Mutex)
		d.files[key] = lock
	}
	return lock
}

// open opens the log of the step at position of the attempt with flag.
func (d *Dir) open(attempt model.AttemptID, position int, flag int) (*os.File, error) {
	f, err := os.OpenFile(d.path(attempt, position), flag, fileMode)
	if err != nil {
		return nil, fmt.Errorf("open the log of step %d of attempt %d: %w", position, attempt, err)
	}
	return f, nil
}

// dir returns the directory of the attempt's logs.
func (d *Dir) dir(attempt model.AttemptID) string {
	return filepath.Join(d.root, strconv.FormatInt(int64(attempt), 10))
}

// path returns the path of the log of the step at position of the attempt.
func (d *Dir) path(attempt model.AttemptID, position int) string {
	return filepath.Join(d.dir(attempt), strconv.Itoa(position)+".log")
}
