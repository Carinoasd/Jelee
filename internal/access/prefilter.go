package access

import (
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

// Glob and regex rules cannot be looked up in a map, so a naive evaluation
// runs every one of them against the value. Most of them require a literal
// substring to match (the "Bot" of "*Bot*", the "-crawler" of a regex), so
// each such rule is filed under its required literals in an Aho-Corasick
// automaton: one pass over the value finds the rules whose literal occurs,
// and only those run their full matcher. Rules without a usable literal are
// still scanned. The prefilter never changes a result, only skips rules
// that cannot match.

// minPrefilterLiteral is the shortest literal worth filing a rule under;
// shorter ones would make the rule a candidate for nearly every value.
const minPrefilterLiteral = 2

// maxRegexLiterals bounds the alternatives a regex may be filed under.
const maxRegexLiterals = 16

// globLiteral returns the longest literal run the glob requires.
func globLiteral(g *globPattern) (string, bool) {
	best := ""
	for _, seg := range g.segs {
		for _, t := range seg {
			if len(t.lit) > len(best) {
				best = t.lit
			}
		}
	}
	return best, len(best) >= minPrefilterLiteral
}

// regexLiterals returns literals one of which every match of pattern (the
// regex as compiled, case folding included) contains. folded reports that
// they are lower case and must be searched in the lower-cased value: a
// case-insensitive part matches any case of its literal. That is sound only
// for ASCII literals without 's' or 'S' (U+017F LONG S folds to 's' but does
// not lower-case to it), so a regex with other folded literals is scanned.
func regexLiterals(pattern string) (set []string, folded, ok bool) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, false, false
	}
	set, folded, ok = requiredLiterals(re.Simplify())
	if !ok || len(set) == 0 || len(set) > maxRegexLiterals {
		return nil, false, false
	}
	for _, lit := range set {
		if len(lit) < minPrefilterLiteral {
			return nil, false, false
		}
		if folded {
			for j := 0; j < len(lit); j++ {
				if b := lit[j]; b >= utf8.RuneSelf || b == 's' {
					return nil, false, false
				}
			}
		}
	}
	slices.Sort(set)
	return slices.Compact(set), folded, true
}

// requiredLiterals computes a set of literals one of which every match of
// re contains, or reports that it cannot. A set with any case-insensitive
// literal is returned lower-cased as a whole (folded): a value containing
// a literal also contains it lower-cased once the value is lower-cased.
func requiredLiterals(re *syntax.Regexp) ([]string, bool, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return []string{strings.ToLower(string(re.Rune))}, true, true
		}
		return []string{string(re.Rune)}, false, true
	case syntax.OpCapture, syntax.OpPlus:
		return requiredLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredLiterals(re.Sub[0])
		}
	case syntax.OpConcat:
		// Any part's literals are required by the whole; prefer the set
		// whose shortest literal is longest, fewer alternatives on a tie.
		var best []string
		bestFolded := false
		bestLen := -1
		for _, sub := range re.Sub {
			set, folded, ok := requiredLiterals(sub)
			if !ok {
				continue
			}
			shortest := len(set[0])
			for _, s := range set {
				shortest = min(shortest, len(s))
			}
			if shortest > bestLen || shortest == bestLen && len(set) < len(best) {
				best, bestFolded, bestLen = set, folded, shortest
			}
		}
		return best, bestFolded, best != nil
	case syntax.OpAlternate:
		var all []string
		anyFolded := false
		for _, sub := range re.Sub {
			set, folded, ok := requiredLiterals(sub)
			if !ok {
				return nil, false, false
			}
			all = append(all, set...)
			anyFolded = anyFolded || folded
			if len(all) > maxRegexLiterals {
				return nil, false, false
			}
		}
		if anyFolded {
			for i := range all {
				all[i] = strings.ToLower(all[i])
			}
		}
		return all, anyFolded, len(all) > 0
	}
	return nil, false, false
}

// literalIndex is an Aho-Corasick automaton over byte strings whose
// patterns map to rules.
type literalIndex struct {
	root  [256]int32
	nodes []acNode
	rules [][]*compiledRule // per pattern
	ids   map[string]int32
}

type acNode struct {
	keys     []byte
	children []int32
	fail     int32
	// out lists the patterns ending here, including through fail links.
	out []int32
}

func (x *literalIndex) add(lit string, c *compiledRule) {
	if x.ids == nil {
		x.ids = map[string]int32{}
		x.nodes = []acNode{{}}
	}
	if id, ok := x.ids[lit]; ok {
		if !slices.Contains(x.rules[id], c) {
			x.rules[id] = append(x.rules[id], c)
		}
		return
	}
	id := int32(len(x.rules))
	x.ids[lit] = id
	x.rules = append(x.rules, []*compiledRule{c})
	node := int32(0)
	for i := 0; i < len(lit); i++ {
		next := x.child(node, lit[i])
		if next < 0 {
			next = int32(len(x.nodes))
			x.nodes = append(x.nodes, acNode{})
			n := &x.nodes[node]
			n.keys, n.children = append(n.keys, lit[i]), append(n.children, next)
		}
		node = next
	}
	x.nodes[node].out = append(x.nodes[node].out, id)
}

func (x *literalIndex) child(node int32, b byte) int32 {
	n := &x.nodes[node]
	for i, k := range n.keys {
		if k == b {
			return n.children[i]
		}
	}
	return -1
}

// build computes the failure links breadth first and the root table.
func (x *literalIndex) build() {
	if x.ids == nil {
		return
	}
	for b := range x.root {
		x.root[b] = 0
	}
	queue := make([]int32, 0, len(x.nodes))
	root := &x.nodes[0]
	for i, k := range root.keys {
		x.root[k] = root.children[i]
		x.nodes[root.children[i]].fail = 0
		queue = append(queue, root.children[i])
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		n := x.nodes[node]
		for i, k := range n.keys {
			child := n.children[i]
			f := n.fail
			for f != 0 && x.child(f, k) < 0 {
				f = x.nodes[f].fail
			}
			fail := x.root[k]
			if f != 0 {
				fail = x.child(f, k)
			}
			if fail == child {
				fail = 0
			}
			x.nodes[child].fail = fail
			x.nodes[child].out = append(x.nodes[child].out, x.nodes[fail].out...)
			queue = append(queue, child)
		}
	}
	x.ids = nil
}

// each calls fn for every rule filed under a literal that occurs in v. A
// rule may be reported more than once.
func (x *literalIndex) each(v string, fn func(*compiledRule)) {
	if len(x.nodes) == 0 {
		return
	}
	state := int32(0)
	for i := 0; i < len(v); i++ {
		b := v[i]
		for {
			if state == 0 {
				state = x.root[b]
				break
			}
			if next := x.child(state, b); next >= 0 {
				state = next
				break
			}
			state = x.nodes[state].fail
		}
		for _, id := range x.nodes[state].out {
			for _, c := range x.rules[id] {
				fn(c)
			}
		}
	}
}
