package admin

import (
	"context"
	"errors"
	"strings"

	"forest/go-api/internal/dnspod"
)

func dnsPodPreflight(code string, err error) error {
	return &dnspod.PreflightError{Code: code, Err: err}
}

func dnsPodDomainName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

// Always verify the name/ID pairing against provider data. The legacy API writes
// by numeric ID while TC3 writes by name: trusting a mismatched pair would make
// the same request target different zones depending on the configured provider.
// Grade is taken from this exact zone, never guessed from a free-plan default.
func resolveDNSPodDomain(ctx context.Context, client dnspodAPI, domain string, domainID int64, needGrade bool) (dnspod.Domain, error) {
	name := dnsPodDomainName(domain)
	if name == "" || domainID < 0 {
		return dnspod.Domain{}, dnsPodPreflight("dns_invalid_arguments", errors.New("invalid domain identity"))
	}
	const pageSize int64 = 100
	const maxPages = 100
	var match *dnspod.Domain
	var offset int64
	for page := 0; page < maxPages; page++ {
		result, err := client.DescribeDomainList(ctx, dnspod.DescribeDomainListRequest{
			Keyword: name, Offset: offset, Limit: pageSize,
		})
		if err != nil {
			return dnspod.Domain{}, dnsPodPreflight("dns_domain_metadata", err)
		}
		for _, candidate := range result.Domains {
			if dnsPodDomainName(candidate.Name) != name && dnsPodDomainName(candidate.Punycode) != name {
				continue
			}
			if candidate.DomainID <= 0 || (domainID > 0 && candidate.DomainID != domainID) || (match != nil && match.DomainID != candidate.DomainID) {
				return dnspod.Domain{}, dnsPodPreflight("dns_domain_metadata", errors.New("domain name and ID mismatch or ambiguous result"))
			}
			copy := candidate
			match = &copy
		}
		// Providers may cap pages below the requested size. Advance by the
		// actual count and use totals when available instead of skipping rows.
		offset += int64(len(result.Domains))
		total := result.Total
		if total == 0 {
			total = result.CountInfo.DomainTotal
		}
		if len(result.Domains) == 0 || (total > 0 && offset >= total) || (total <= 0 && len(result.Domains) < int(pageSize)) {
			if match == nil || (needGrade && strings.TrimSpace(match.Grade) == "") {
				return dnspod.Domain{}, dnsPodPreflight("dns_domain_metadata", errors.New("exact domain or actual grade unavailable"))
			}
			match.Grade = strings.TrimSpace(match.Grade)
			return *match, nil
		}
	}
	return dnspod.Domain{}, dnsPodPreflight("dns_domain_metadata", errors.New("domain lookup pagination limit reached"))
}
