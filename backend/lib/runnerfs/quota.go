package runnerfs

import (
	"context"
	"sort"

	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// QuotaToProto flattens a per-CLI quota map (agentwrapper.GetQuota) into the
// wire form, sorted by cli then model. Every entry is marked enabled: the
// quota query already honors the runner's provider configuration, so a listed
// model is one the runner is willing to run.
func QuotaToProto(res map[string][]llms.ModelUsage) []*runnerpb.ModelUsage {
	clis := make([]string, 0, len(res))
	for cli := range res {
		clis = append(clis, cli)
	}
	sort.Strings(clis)
	var out []*runnerpb.ModelUsage
	for _, cli := range clis {
		for _, u := range res[cli] {
			m := &runnerpb.ModelUsage{
				Cli: cli, Model: u.Model, Remaining: u.Remaining, RefreshDate: u.RefreshDate, Enabled: true,
			}
			for _, l := range u.Limits {
				m.Limits = append(m.Limits, &runnerpb.QuotaLimit{Name: l.Name, Remaining: l.Remaining, RefreshDate: l.RefreshDate})
			}
			if u.Balance != nil {
				b := &runnerpb.AccountBalance{IsAvailable: u.Balance.IsAvailable}
				for _, bi := range u.Balance.BalanceInfos {
					b.BalanceInfos = append(b.BalanceInfos, &runnerpb.BalanceInfo{
						Currency: bi.Currency, TotalBalance: bi.TotalBalance,
						GrantedBalance: bi.GrantedBalance, ToppedUpBalance: bi.ToppedUpBalance,
					})
				}
				m.Balance = b
			}
			out = append(out, m)
		}
	}
	return out
}

// QuotaFromProto is the inverse of QuotaToProto. Disabled entries are
// dropped, mirroring the filtered map the previous local query returned.
func QuotaFromProto(usages []*runnerpb.ModelUsage) map[string][]llms.ModelUsage {
	res := make(map[string][]llms.ModelUsage)
	for _, m := range usages {
		if !m.GetEnabled() {
			continue
		}
		u := llms.ModelUsage{Model: m.GetModel(), Remaining: m.GetRemaining(), RefreshDate: m.GetRefreshDate()}
		for _, l := range m.GetLimits() {
			u.Limits = append(u.Limits, llms.QuotaLimit{Name: l.GetName(), Remaining: l.GetRemaining(), RefreshDate: l.GetRefreshDate()})
		}
		if b := m.GetBalance(); b != nil {
			ab := &llms.AccountBalance{IsAvailable: b.GetIsAvailable(), BalanceInfos: []llms.BalanceInfo{}}
			for _, bi := range b.GetBalanceInfos() {
				ab.BalanceInfos = append(ab.BalanceInfos, llms.BalanceInfo{
					Currency: bi.GetCurrency(), TotalBalance: bi.GetTotalBalance(),
					GrantedBalance: bi.GetGrantedBalance(), ToppedUpBalance: bi.GetToppedUpBalance(),
				})
			}
			u.Balance = ab
		}
		res[m.GetCli()] = append(res[m.GetCli()], u)
	}
	return res
}

// QueryQuota asks a runner for its quota (all targets) in the shape
// agentwrapper.GetQuota returns.
func QueryQuota(ctx context.Context, client runnerpb.RunnerServiceClient) (map[string][]llms.ModelUsage, error) {
	resp, err := client.QueryQuota(ctx, &runnerpb.QueryQuotaRequest{})
	if err != nil {
		return nil, ToError(err)
	}
	return QuotaFromProto(resp.GetUsages()), nil
}
