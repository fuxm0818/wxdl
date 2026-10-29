//go:build !unix

package flow

// lockInstance has nothing to lock where wxdl cannot capture anyway: Prepare
// refuses to run on any platform other than macOS. It exists so the package
// still builds there.
func (s *Session) lockInstance() (func(), error) { return func() {}, nil }
