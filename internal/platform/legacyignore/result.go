package legacyignore

import (
	"context"
	"encoding/binary"
	"errors"
)

var ErrResult = errors.New("legacy_ignore_invalid_result")

type DecisionKind byte

const (
	NoMatch DecisionKind = iota
	RuleInclude
	RuleExclude
	BlankExclude
	InvalidSourceExclude
)

type Decision struct {
	Kind DecisionKind
	Line int
}
type BatchResult struct {
	InvalidLines []int
	Decisions    []Decision
}

const MaxResultBytes = 12 + 4*MaxSourceLines + 5*MaxBatchPaths

// ValidateResult binds provenance to the original request. Runtime errors are
// deliberately not successful decisions: cancellation, limits and crashes
// must return an error to the caller, which records an unknown observation.
func ValidateResult(ctx context.Context, batch Batch, result BatchResult) error {
	if ctx == nil {
		return ErrResult
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := EncodeBatch(ctx, batch); err != nil {
		return err
	}
	source, err := PrepareSource(ctx, batch.Source)
	if err != nil {
		return err
	}
	if len(result.Decisions) != len(batch.Paths) || len(result.InvalidLines) > len(source.Rules) {
		return ErrResult
	}
	rules := make(map[int]Expression, len(source.Rules))
	for _, r := range source.Rules {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := Translate(r.Text)
		if err != nil {
			return ErrResult
		}
		rules[r.Line] = e
	}
	invalid := make(map[int]bool, len(result.InvalidLines))
	previous := 0
	for _, line := range result.InvalidLines {
		e, ok := rules[line]
		if !ok || e.Inactive || line <= previous {
			return ErrResult
		}
		invalid[line] = true
		previous = line
	}
	allInvalid := !source.Blank && len(source.Rules) > 0 && len(invalid) == len(source.Rules)
	for _, d := range result.Decisions {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch d.Kind {
		case NoMatch:
			if source.Blank || allInvalid || d.Line != 0 {
				return ErrResult
			}
		case BlankExclude:
			if !source.Blank || d.Line != 0 {
				return ErrResult
			}
		case InvalidSourceExclude:
			if !allInvalid || d.Line != 0 {
				return ErrResult
			}
		case RuleInclude, RuleExclude:
			rule, ok := rules[d.Line]
			if !ok || rule.Inactive || invalid[d.Line] || (d.Kind == RuleInclude) != rule.Negative {
				return ErrResult
			}
		default:
			return ErrResult
		}
	}
	return nil
}

func EncodeResult(ctx context.Context, batch Batch, result BatchResult) ([]byte, error) {
	if err := ValidateResult(ctx, batch, result); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 12+4*len(result.InvalidLines)+5*len(result.Decisions))
	out = append(out, "JIR1"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(result.InvalidLines)))
	for _, line := range result.InvalidLines {
		out = binary.LittleEndian.AppendUint32(out, uint32(line))
	}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(result.Decisions)))
	for _, d := range result.Decisions {
		out = append(out, byte(d.Kind))
		out = binary.LittleEndian.AppendUint32(out, uint32(d.Line))
	}
	return out, nil
}

func DecodeResult(ctx context.Context, batch Batch, data []byte) (BatchResult, error) {
	if ctx == nil {
		return BatchResult{}, ErrResult
	}
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	if len(data) < 12 || len(data) > MaxResultBytes || string(data[:4]) != "JIR1" {
		return BatchResult{}, ErrResult
	}
	count := uint64(binary.LittleEndian.Uint32(data[4:8]))
	data = data[8:]
	if count > MaxSourceLines || count*4+4 > uint64(len(data)) {
		return BatchResult{}, ErrResult
	}
	result := BatchResult{InvalidLines: make([]int, int(count))}
	for i := range result.InvalidLines {
		result.InvalidLines[i] = int(binary.LittleEndian.Uint32(data[:4]))
		data = data[4:]
	}
	count = uint64(binary.LittleEndian.Uint32(data[:4]))
	data = data[4:]
	if count < 1 || count > MaxBatchPaths || count*5 != uint64(len(data)) {
		return BatchResult{}, ErrResult
	}
	result.Decisions = make([]Decision, int(count))
	for i := range result.Decisions {
		result.Decisions[i] = Decision{Kind: DecisionKind(data[0]), Line: int(binary.LittleEndian.Uint32(data[1:5]))}
		data = data[5:]
	}
	if err := ValidateResult(ctx, batch, result); err != nil {
		return BatchResult{}, err
	}
	return result, nil
}
