package core

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ObservationEvents supplies hints, never queue authority. There is no expiry
// timer or degraded polling fallback. Overflow/watch loss requires reconnect.
// Install before the first snapshot to close the subscribe/read race.
type ObservationEvents struct {
	Changes <-chan struct{}
	Errors  <-chan error
	cancel  context.CancelFunc
	done    chan struct{}
}

// Advisory only: no revision, receipt, journal or queue state lives here.
// WAL writes can precede SQLite's published commit; supported OCQ writers also
// emit this hint AFTER COMMIT returns, including long-lived dispatchers. Never
// turn a failed hint into a failed/automatically retried committed operation.
func signalObservationCommit(dir string) {
	f, err := privateFile(filepath.Join(dir, "observation.notify"), os.O_WRONLY)
	if err != nil {
		// Absent without an observer, or advisory failure after a commit.
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 || info.Size() > 1 {
		return
	}
	_, _ = f.WriteAt([]byte{1}, 0)
}

func (w *ObservationEvents) Close() { w.cancel(); <-w.done }

func WatchObservationEvents(parent context.Context, dir string) (*ObservationEvents, error) {
	return watchObservationEvents(parent, dir, "")
}

// A native helper must also wake after the binder releases its write lock:
// the immutable record event may arrive while that lock is still held.
// Ordinary projections ignore these events to avoid read-side feedback.
func watchObservationEvents(parent context.Context, dir string, lockJournal string) (*ObservationEvents, error) {
	dir, err := resolveStateDir(dir)
	if err != nil {
		return nil, err
	}
	reader, err := OpenReader(dir) // Existing store only; never mkdir/init/migrate.
	if err != nil {
		return nil, err
	}
	hint, err := privateFile(filepath.Join(dir, "observation.notify"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		reader.Close()
		return nil, err
	}
	info, err := hint.Stat()
	hint.Close()
	if err != nil || info.Size() > 1 {
		reader.Close()
		return nil, errors.New("invalid observation notification hint")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		reader.Close()
		return nil, errors.New("observation notification hint must not be hard linked")
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		reader.Close()
		return nil, err
	}
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(fd)
		reader.Close()
		return nil, err
	}
	watched := map[string]bool{}
	rootWD := -1
	install := func() error {
		return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if parent.Err() != nil {
				return parent.Err()
			}
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if !entry.IsDir() {
				return nil
			} // Never follows symlinks.
			if path == filepath.Join(dir, "worktrees") {
				return filepath.SkipDir
			}
			if watched[path] {
				return nil
			}
			if len(watched) >= 1024 {
				return errors.New("push watch directory limit exceeded")
			}
			wd, err := unix.InotifyAddWatch(fd, path, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT|unix.IN_ONLYDIR|unix.IN_DONT_FOLLOW)
			if err != nil {
				return err
			}
			if path == dir {
				rootWD = wd
			}
			watched[path] = true
			return nil
		})
	}
	if err := install(); err != nil {
		unix.Close(wake)
		unix.Close(fd)
		reader.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	changes, failures := make(chan struct{}, 1), make(chan error, 1)
	w := &ObservationEvents{changes, failures, cancel, make(chan struct{})}
	wakerDone := make(chan struct{})
	go func() {
		defer close(wakerDone)
		<-ctx.Done()
		var one [8]byte
		binary.NativeEndian.PutUint64(one[:], 1)
		_, _ = unix.Write(wake, one[:])
	}()
	go func() {
		// Retain a read-only connection for this watch's lifetime. Otherwise
		// SQLite's last-reader WAL/SHM creation/removal can feed back into the
		// filesystem hints produced by the very next projection read.
		defer func() {
			cancel()
			<-wakerDone
			unix.Close(wake)
			unix.Close(fd)
			reader.Close()
			close(w.done)
		}()
		buf := make([]byte, 64<<10)
		fail := func(err error) { failures <- err }
		for {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(wake), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, -1); err != nil {
				if errors.Is(err, unix.EINTR) {
					continue
				}
				fail(err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				fail(errors.New("push filesystem notification lost"))
				return
			}
			dirty, newDir := false, false
			for batch := 0; batch < 8; batch++ {
				if ctx.Err() != nil {
					return
				}
				n, err := unix.Read(fd, buf)
				if errors.Is(err, unix.EAGAIN) {
					break
				}
				if err != nil {
					fail(err)
					return
				}
				if n == 0 {
					fail(errors.New("push filesystem notification ended"))
					return
				}
				for pos := 0; pos+unix.SizeofInotifyEvent <= n; {
					wd := int32(binary.NativeEndian.Uint32(buf[pos:]))
					mask := binary.NativeEndian.Uint32(buf[pos+4:])
					length := int(binary.NativeEndian.Uint32(buf[pos+12:]))
					end := pos + unix.SizeofInotifyEvent + length
					if end > n {
						fail(errors.New("invalid push filesystem event"))
						return
					}
					name := strings.TrimRight(string(buf[pos+unix.SizeofInotifyEvent:end]), "\x00")
					pos = end
					if mask&(unix.IN_Q_OVERFLOW|unix.IN_IGNORED|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT) != 0 {
						fail(errors.New("push watch reset; explicit reconnect required"))
						return
					}
					if mask&unix.IN_ISDIR != 0 {
						newDir = true
					}
					if int(wd) == rootWD && name == "observation.notify" && mask&(unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_ATTRIB) != 0 {
						fail(errors.New("push commit notification resource changed"))
						return
					}
					if int(wd) == rootWD && name == "queue.db-wal" && mask&unix.IN_MODIFY == 0 {
						// A read-only SQLite open can create/close/unlink an
						// empty WAL. Only WAL writes are commit hints.
						continue
					}
					// Read-only SQLite connections may touch SHM. Locks and SHM
					// are not authority changes and must not feed back into reads.
					if name == "queue.db-shm" || strings.HasSuffix(name, ".lock") || name == "lock" {
						if lockJournal != "" && mask&unix.IN_CLOSE_WRITE != 0 && (name == "lock" || name == lockJournal+".lock") {
							dirty = true
						}
						continue
					}
					dirty = true
				}
			}
			if newDir {
				if err := install(); err != nil {
					fail(err)
					return
				}
			}
			if dirty {
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	return w, nil
}
