package model

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// Repository is the identity of a repository: its host, its organization
// and its name. It is parsed once from a URL, and the URL is not kept.
type Repository struct {
	// Host is lowercase and has no port.
	Host string
	// Organization is every path segment before the name, nested groups
	// included, in the case the URL gives.
	Organization string
	// Name has no .git suffix, in the case the URL gives.
	Name string
}

// ParseRepository returns the identity of the repository at the URL s. It
// accepts the https, http, ssh, git+ssh and git schemes and git's scp-like
// form (git@host:org/repo.git). It drops the user, the credential, the port,
// the query, the fragment and one .git suffix, and puts nested groups in the
// organization. Its errors never repeat s, which can carry a credential.
func ParseRepository(s string) (Repository, error) {
	if hasSpaceOrControl(s) {
		return Repository{}, errors.New("repository URL contains whitespace or a control character")
	}
	host, path, err := splitRemote(s)
	if err != nil {
		return Repository{}, err
	}
	if host == "" {
		return Repository{}, errors.New("repository URL has no host")
	}
	path = strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || hasSpaceOrControl(segment) {
			return Repository{}, errors.New("repository URL has an empty, '.', '..' or blank path segment")
		}
		// No forge allows '@' or ':' in a name; in a segment they are what is
		// left of a user and a credential that a '/' hid from the split.
		if strings.ContainsAny(segment, "@:") {
			return Repository{}, errors.New("repository URL has '@' or ':' in a path segment")
		}
	}
	last := len(segments) - 1
	if last == 0 {
		return Repository{}, errors.New("repository URL has no organization")
	}
	return Repository{
		Host:         strings.ToLower(host),
		Organization: strings.Join(segments[:last], "/"),
		Name:         segments[last],
	}, nil
}

// String returns host/organization/name.
func (r Repository) String() string {
	return r.Host + "/" + r.Organization + "/" + r.Name
}

// splitRemote returns the host, without user or port, and the path of the
// URL s.
func splitRemote(s string) (string, string, error) {
	if !strings.Contains(s, "://") {
		return splitSCP(s)
	}
	// url.Parse's errors quote the whole URL, credential included.
	u, err := url.Parse(s)
	if err != nil {
		return "", "", errors.New("repository URL is not a valid URL")
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git+ssh", "git":
		return u.Hostname(), u.Path, nil
	default:
		return "", "", errors.New("repository URL has an unsupported scheme")
	}
}

// splitSCP returns the host and the path of git's scp-like form
// [user@]host:path, which git reads when a ':' comes before the first '/'.
// Anything else without a scheme is a local path.
func splitSCP(s string) (string, string, error) {
	beforePath, _, _ := strings.Cut(s, "/")
	// The user, and any credential in it, ends at the last '@' before the path.
	rest := s[strings.LastIndex(beforePath, "@")+1:]
	host, path, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(host, "/") {
		return "", "", errors.New("repository URL is a local path, not a remote")
	}
	return host, path, nil
}

// hasSpaceOrControl reports whether s has a whitespace or control character.
func hasSpaceOrControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}
