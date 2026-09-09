package service

//go:generate go run ../../cmd/gen-units -out ../../packaging

// The packaged copies of these units live in ../../packaging and are generated from this package,
// not maintained beside it. service_test.go fails if they differ, so the Debian package, the tarball
// and `x1200 systemd` cannot come to disagree about what the unit says — which would otherwise show
// up as a machine sampling differently depending on how the tool was installed, weeks later, with
// nothing to point at.
