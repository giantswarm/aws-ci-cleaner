package azure

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/dns/mgmt/2017-10-01/dns"
	"github.com/bogdanovich/dns_resolver"
	"github.com/giantswarm/microerror"
)

const (
	dnsFailureError    = "SERVFAIL"
	dnsServerAddress   = "8.8.8.8"
	e2eterraformPrefix = "e2eterraform"
	resourceGroup      = "root_dns_zone_rg"
	zoneName           = "azure.gigantic.io"
)

func (c Cleaner) cleanDelegateDNSRecords(ctx context.Context) error {
	var lastError error

	recordsIter, err := c.dnsRecordSetsClient.ListAllByDNSZoneComplete(ctx, resourceGroup, zoneName, nil, "")
	if err != nil {
		return microerror.Mask(err)
	}

	deadLine := time.Now().Add(-gracePeriod).UTC()

	for ; recordsIter.NotDone(); recordsIter.Next() {
		record := recordsIter.Value()

		del, err := c.dnsRecordShouldBeDeleted(ctx, record, deadLine)
		if err != nil {
			c.logger.LogCtx(ctx, "level", "error", "message", fmt.Sprintf("failed to check DNS record %q", *record.Name), "stack", fmt.Sprintf("%#v", microerror.Mask(err)))
			c.logger.LogCtx(ctx, "level", "error", "message", "skipping")
			lastError = err
			continue
		}

		if del {
			c.logger.LogCtx(ctx, "level", "info", "message", fmt.Sprintf("DNS record %s has to be deleted", *record.Name))
			err := c.deleteRecord(ctx, record)
			if err != nil {
				c.logger.LogCtx(ctx, "level", "error", "message", fmt.Sprintf("failed to delete DNS record %q", *record.Name), "stack", fmt.Sprintf("%#v", microerror.Mask(err)))
				c.logger.LogCtx(ctx, "level", "error", "message", "skipping")
				lastError = err
				continue
			}

			c.logger.LogCtx(ctx, "level", "debug", "info", fmt.Sprintf("DNS record %s was deleted", *record.Name))
		} else {
			c.logger.LogCtx(ctx, "level", "debug", "message", fmt.Sprintf("DNS record %s has to be kept", *record.Name))
		}

	}

	if lastError != nil {
		return microerror.Mask(lastError)
	}

	return nil
}

func (c Cleaner) deleteRecord(ctx context.Context, dnsRecord dns.RecordSet) error {
	_, err := c.dnsRecordSetsClient.Delete(ctx, resourceGroup, zoneName, *dnsRecord.Name, dns.NS, *dnsRecord.Etag)

	return err
}

func (c Cleaner) dnsRecordShouldBeDeleted(ctx context.Context, dnsRecord dns.RecordSet, since time.Time) (bool, error) {
	if !isCIRecord(*dnsRecord.Name) {
		return false, nil
	}

	resolves, err := resolvesApiName(*dnsRecord.Name)
	if err != nil {
		c.logger.LogCtx(ctx, "level", "warning", "message", fmt.Sprintf("Unexpected error when trying to resolve %s: %s", *dnsRecord.Name, err.Error()))
		return false, nil
	}

	return !resolves, nil
}

// isCIRecord checks if resource group name was created by a CI pipeline.
func isCIRecord(s string) bool {
	if strings.HasPrefix(s, e2eterraformPrefix) {
		return true
	}

	// Match strings like:
	// e2eabcd.westeurope
	re := regexp.MustCompile(`^e2e.*\.(westeurope|germanywestcentral)$`)

	return re.Match([]byte(s))
}

// Tries to resolve the API hostname on the specified delegated zone.
func resolvesApiName(name string) (bool, error) {
	full := fmt.Sprintf("api.%s.%s", name, zoneName)

	resolver := dns_resolver.New([]string{dnsServerAddress})

	// In case of i/o timeout
	resolver.RetryTimes = 5

	addresses, err := resolver.LookupHost(full)
	if err != nil {
		if !strings.Contains(err.Error(), dnsFailureError) {
			return false, err
		}
	}

	return len(addresses) > 0, nil
}
