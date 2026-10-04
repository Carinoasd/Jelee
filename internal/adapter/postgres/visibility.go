package postgres

// The unified authorization filter (G48.2, G48.8). Every user-facing read
// of catalog content binds its rows to the caller in SQL through the
// predicates of this file, never by filtering results afterwards. Rules are
// added here only; TestVisibilityPredicateHasOneSource fails when another
// statement reads the grant tables itself.
//
// The predicates read the live user row as u: a statement either joins
// users u (enabled, not deleted) or selects principalColumnsSQL into a
// principal CTE and reads it as u.

// visibilityUserColumns are the user columns the predicates read.
const visibilityUserColumns = `id,is_admin`

// principalColumnsSQL lists visibilityUserColumns qualified by alias, for
// principal CTEs that select from users under that alias.
func principalColumnsSQL(alias string) string {
	return alias + `.id,` + alias + `.is_admin`
}

// libraryVisibleSQL is the library grant: administrators see every library,
// anyone else only the libraries granted in library_acl. column names the
// library being checked.
func libraryVisibleSQL(column string) string {
	return `(u.is_admin OR EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=` + column + `))`
}

// itemVisibleSQL is the visibility of one catalog item: library names its
// library column and item its item ID column. Sources, sidecar tracks,
// images, user data and statistics rows are visible exactly when their
// item is.
func itemVisibleSQL(library, item string) string {
	_ = item
	return libraryVisibleSQL(library)
}

// grantedLibrariesSQL selects the library_id of every library granted to the
// non-administrator user alias, so a listing can walk granted libraries
// instead of filtering every item.
func grantedLibrariesSQL(alias string) string {
	return `SELECT a.library_id FROM library_acl a WHERE a.user_id=` + alias + `.id`
}
