package reconcile

import (
	"sort"

	"github.com/umars28/marspay/internal/money"
)

type Cause string

const (
	CauseProviderOnly   Cause = "provider_only"
	CauseInternalOnly   Cause = "internal_only"
	CauseAmountMismatch Cause = "amount_mismatch"
)

func (c Cause) Explain() string {
	switch c {
	case CauseProviderOnly:
		return "the provider settled it but we never recorded it, most likely a lost callback"
	case CauseInternalOnly:
		return "we recorded it but the provider statement does not show it yet"
	case CauseAmountMismatch:
		return "both sides know the reference but disagree on the amount"
	default:
		return "unknown"
	}
}

type Line struct {
	Ref    string
	Amount money.Minor
}

type Discrepancy struct {
	Ref      string
	Internal *money.Minor
	Provider *money.Minor
	Delta    money.Minor
	Cause    Cause
}

type Report struct {
	RowsCompared  int
	RowsMatched   int
	Discrepancies []Discrepancy
	Delta         money.Minor
}

func (r Report) Clean() bool {
	return len(r.Discrepancies) == 0
}

func Compare(internal, provider []Line) Report {
	internalByRef := index(internal)
	providerByRef := index(provider)

	refs := make(map[string]struct{}, len(internalByRef)+len(providerByRef))
	for ref := range internalByRef {
		refs[ref] = struct{}{}
	}
	for ref := range providerByRef {
		refs[ref] = struct{}{}
	}

	ordered := make([]string, 0, len(refs))
	for ref := range refs {
		ordered = append(ordered, ref)
	}
	sort.Strings(ordered)

	report := Report{RowsCompared: len(ordered)}

	for _, ref := range ordered {
		in, hasInternal := internalByRef[ref]
		pr, hasProvider := providerByRef[ref]

		switch {
		case hasInternal && hasProvider && in == pr:
			report.RowsMatched++

		case hasInternal && hasProvider:
			delta := in - pr
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Ref: ref, Internal: &in, Provider: &pr,
				Delta: delta, Cause: CauseAmountMismatch,
			})
			report.Delta += delta

		case hasProvider:
			delta := -pr
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Ref: ref, Provider: &pr,
				Delta: delta, Cause: CauseProviderOnly,
			})
			report.Delta += delta

		default:
			delta := in
			report.Discrepancies = append(report.Discrepancies, Discrepancy{
				Ref: ref, Internal: &in,
				Delta: delta, Cause: CauseInternalOnly,
			})
			report.Delta += delta
		}
	}

	return report
}

func index(lines []Line) map[string]money.Minor {
	out := make(map[string]money.Minor, len(lines))
	for _, l := range lines {
		out[l.Ref] += l.Amount
	}
	return out
}
