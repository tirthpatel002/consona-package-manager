package npm

// ExtraScopes, when set (by the daemon), adds scopes that are private
// besides the ones routed in the user npmrc: scopes of folders registered
// on this device and of packages seen on SSH remotes. They are served by
// consona, withheld from audits and refused for writes, exactly like
// routed scopes.
var ExtraScopes func() map[string]bool

// PrivateScopes is RoutedScopes plus ExtraScopes.
func PrivateScopes() map[string]bool {
	return privateScopes()
}

// privateScopes is RoutedScopes plus ExtraScopes.
func privateScopes() map[string]bool {
	s := RoutedScopes()
	if ExtraScopes != nil {
		for k := range ExtraScopes() {
			s[k] = true
		}
	}
	return s
}
