package ignore

// match advances a bounded NFA, without recursion, backtracking or branch
// copies. Star epsilon edges point only forward, so one left-to-right pass
// computes closure. Both buffers are reused for every rule in this evaluation.
func (p *Program) match(r rule, text string, m *workMeter, current, next []bool) (bool, error) {
	if r.invalid || r.count == 0 {
		return false, nil
	}
	current = current[:r.count+1]
	next = next[:r.count+1]
	for i := range current {
		if err := m.spend(1); err != nil {
			return false, err
		}
		current[i] = false
		next[i] = false
	}
	current[0] = true
	for position := 0; position <= len(text); position++ {
		var c byte
		if position < len(text) {
			c = text[position]
			if p.mode == CaseASCIIInsensitive {
				c = fold(c)
			}
		}
		for state := 0; state <= r.count; state++ {
			if err := m.spend(1); err != nil {
				return false, err
			}
			if !current[state] {
				continue
			}
			if state == r.count {
				if position == len(text) {
					return true, nil
				}
				continue
			}
			t := p.tokens[r.start+state]
			switch t.kind {
			case star, globstar:
				current[state+1] = true
			case componentStar:
				if position == 0 || text[position-1] == '/' {
					current[state+1] = true
				}
			}
			if position == len(text) {
				continue
			}
			switch t.kind {
			case literal:
				if c == t.value {
					next[state+1] = true
				}
			case oneByte:
				if c != '/' {
					next[state+1] = true
				}
			case byteClass:
				if c != '/' && p.classes[t.class].contains(c) {
					next[state+1] = true
				}
			case star:
				if c != '/' {
					next[state] = true
				}
			case globstar, componentStar:
				next[state] = true
			}
		}
		current, next = next, current
		for i := range next {
			if err := m.spend(1); err != nil {
				return false, err
			}
			next[i] = false
		}
	}
	return false, nil
}
