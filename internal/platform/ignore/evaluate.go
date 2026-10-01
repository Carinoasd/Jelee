package ignore

import "context"

// Evaluate first proves that each ancestor remains traversable. Rules inside
// an excluded directory cannot re-include that directory or its descendants.
func (p *Program) Evaluate(ctx context.Context, path string, kind Kind) (result Match, err error) {
	if ctx == nil {
		return Match{}, ErrInvalid
	}
	defer func() {
		if e := ctx.Err(); e != nil {
			result, err = Match{}, e
		}
		if err != nil {
			result = Match{}
		}
	}()
	if err = ctx.Err(); err != nil {
		return Match{}, err
	}
	if p == nil || !p.valid || kind != File && kind != Directory {
		return Match{}, ErrInvalid
	}
	m := newWorkMeter(ctx, MaxEvaluateWork)
	if _, err = validatePath(path, m); err != nil {
		return Match{}, err
	}
	current, next := make([]bool, p.maxPattern+1), make([]bool, p.maxPattern+1)
	active := make([]int, 0, MaxPathComponents)
	if index, ok := p.byBase[""]; ok {
		active = append(active, index)
	}
	start := 0
	for end := 0; end <= len(path); end++ {
		if err = m.spend(1); err != nil {
			return Match{}, err
		}
		if end < len(path) && path[end] != '/' {
			continue
		}
		if err = m.check(); err != nil {
			return Match{}, err
		}
		prefix := path[:end]
		isDirectory := end < len(path) || kind == Directory
		decision := Match{}
		for _, index := range active {
			if err = m.spend(1); err != nil {
				return Match{}, err
			}
			source := p.sources[index]
			relative := prefix
			if source.base != "" {
				relative = prefix[len(source.base)+1:]
			}
			for i := source.start; i < source.end; i++ {
				if err = m.check(); err != nil {
					return Match{}, err
				}
				if err = m.spend(1); err != nil {
					return Match{}, err
				}
				r := p.rules[i]
				if r.directory && !isDirectory {
					continue
				}
				candidate := relative
				if r.basename {
					candidate = path[start:end]
				}
				matched, e := p.match(r, candidate, m, current, next)
				if e != nil {
					return Match{}, e
				}
				if matched {
					outcome := Exclude
					if r.negative {
						outcome = Include
					}
					decision = Match{Outcome: outcome, Source: source.path, Line: r.line, MatchedPath: prefix}
				}
			}
		}
		if decision.Outcome == Exclude {
			decision.ParentBlocked = end < len(path)
			return decision, nil
		}
		if end == len(path) {
			return decision, nil
		}
		// Source activation uses exact validated components, even when pattern
		// comparisons fold ASCII. Filesystem name resolution is a future layer.
		if err = m.spend(len(prefix)); err != nil {
			return Match{}, err
		}
		if index, ok := p.byBase[prefix]; ok {
			active = append(active, index)
		}
		start = end + 1
	}
	return Match{}, nil
}
