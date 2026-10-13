package model

import (
	"strings"
	"testing"
)

// secret is the credential the refusal cases carry, which no error may
// repeat.
const secret = "s3cr3t"

func TestParseRepository(t *testing.T) {
	cases := map[string]Repository{
		"https://user:token@gitlab.com/group/sub/repo.git": {Host: "gitlab.com", Organization: "group/sub", Name: "repo"},
		"git@github.com:thatsnotmynameio/crew-actions.git": {
			Host: "github.com", Organization: "thatsnotmynameio", Name: "crew-actions",
		},
		"https://github.com/thatsnotmynameio/crew-actions": {
			Host: "github.com", Organization: "thatsnotmynameio", Name: "crew-actions",
		},
		"ssh://git@GitHub.com:22/org/repo.git/":           {Host: "github.com", Organization: "org", Name: "repo"},
		"git://host/org/repo":                             {Host: "host", Organization: "org", Name: "repo"},
		"http://host/org/repo":                            {Host: "host", Organization: "org", Name: "repo"},
		"git+ssh://git@host/org/repo.git":                 {Host: "host", Organization: "org", Name: "repo"},
		"https://host/org/repo.git?private_token=x#frag":  {Host: "host", Organization: "org", Name: "repo"},
		"user:" + secret + "@host:org/repo.git":           {Host: "host", Organization: "org", Name: "repo"},
		"https://host/Org/Repo.git.git":                   {Host: "host", Organization: "Org", Name: "Repo.git"},
		"https://user:" + secret + "@host/org/repo.git//": {Host: "host", Organization: "org", Name: "repo"},
	}
	for in, want := range cases {
		got, err := ParseRepository(in)
		if err != nil {
			t.Errorf("ParseRepository(%q) error = %v, want nil", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRepository(%q) = %+v, want %+v", in, got, want)
		}
		if strings.Contains(got.String(), secret) || strings.Contains(got.String(), "token") {
			t.Errorf("ParseRepository(%q).String() = %q, keeps the credential", in, got)
		}
	}
}

func TestRepositoryString(t *testing.T) {
	r := Repository{Host: "gitlab.com", Organization: "group/sub", Name: "repo"}
	if got, want := r.String(), "gitlab.com/group/sub/repo"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseRepositoryRefuses(t *testing.T) {
	refused := []string{
		"https://host/repo.git",
		"https://host/org//repo",
		"https://host/org/../repo",
		"file:///srv/repo.git",
		"/srv/repo",
		"../repo",
		"",
		// Each refusal branch, with a credential in the input.
		"https://user:" + secret + "@host/repo.git",
		"https://user:" + secret + "@host/org//repo",
		"https://user:" + secret + "@host/org/./repo",
		"https://user:" + secret + "@host/org/%2e%2e/repo",
		"https://user:" + secret + "@host/org/.git",
		"https://user:" + secret + "@/org/repo",
		"https://user:" + secret + "@host/org/re%00po",
		"https://user:" + secret + "@host/org/re%20po",
		"https://user:tok%zz@host/org/repo",
		"https://user:" + secret + "%zz@host/org/repo",
		"ftp://user:" + secret + "@host/org/repo",
		"file://user:" + secret + "@host/org/repo",
		" https://user:" + secret + "@host/org/repo",
		"https://user:" + secret + "@host/org/repo\n",
		"user:" + secret + "@host/org/repo",
		"user:" + secret + "@:org/repo",
		"user:" + secret + "@host:repo",
		"user:" + secret + "@host:org//repo",
	}
	for _, in := range refused {
		got, err := ParseRepository(in)
		if err == nil {
			t.Errorf("ParseRepository(%q) = %+v, want an error", in, got)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, secret) || strings.Contains(msg, "tok") {
			t.Errorf("ParseRepository(%q) error = %q, repeats the credential", in, msg)
		}
	}
}
