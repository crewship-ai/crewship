package dockerutil

import (
	"context"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

type resolvingKeychain func(authn.Resource) (authn.Authenticator, error)

func (f resolvingKeychain) Resolve(resource authn.Resource) (authn.Authenticator, error) {
	return f(resource)
}

func TestNonLoopbackRegistryAuthentication(t *testing.T) {
	ref, err := name.ParseReference("registry.example.invalid/team/image:latest")
	if err != nil {
		t.Fatal(err)
	}
	want := authn.FromConfig(authn.AuthConfig{Username: "test-user"})
	for _, tc := range []struct {
		name string
		auth authn.Authenticator
		err  error
		want authn.Authenticator
	}{
		{"valid credential", want, nil, want},
		{"broken credential helper", nil, errTestKeychain, authn.Anonymous},
		{"empty credential helper", nil, nil, authn.Anonymous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewDigestResolver(0, 0, WithKeychain(resolvingKeychain(func(resource authn.Resource) (authn.Authenticator, error) {
				if resource.RegistryStr() != "registry.example.invalid" {
					t.Errorf("credential requested for %q", resource.RegistryStr())
				}
				return tc.auth, tc.err
			})))
			if got := r.authFor(context.Background(), ref); got != tc.want {
				t.Fatalf("auth = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlockedRegistryCredentialHelperHonorsCancellation(t *testing.T) {
	ref, err := name.ParseReference("registry.example.invalid/team/image:latest")
	if err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := NewDigestResolver(0, 0, WithKeychain(resolvingKeychain(func(authn.Resource) (authn.Authenticator, error) {
		close(started)
		<-release
		close(finished)
		return nil, nil
	})))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan authn.Authenticator, 1)
	go func() { result <- r.authFor(ctx, ref) }()
	<-started
	cancel()
	got := <-result
	close(release)
	<-finished
	if got != authn.Anonymous {
		t.Fatal("cancelled credential resolution did not fall back to anonymous")
	}
}
