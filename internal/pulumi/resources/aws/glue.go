package aws

import (
	"strconv"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
	"github.com/shopspring/decimal"
)

const glueServiceCode = "AWSGlue"

func glueEntry(record resources.ResourceRecord, region, inputsJSON, subLabel string, extra map[string]string, props map[string]string) resources.DecodedResource {
	a := map[string]string{
		"servicecode":   glueServiceCode,
		"productFamily": "AWS Glue",
	}
	for k, v := range extra {
		a[k] = v
	}
	return resources.DecodedResource{
		Provider:   "aws",
		Region:     region,
		Service:    "other",
		Name:       record.Name,
		SubLabel:   subLabel,
		RawType:    record.Type,
		Attrs:      a,
		Props:      props,
		InputsJSON: inputsJSON,
	}
}

// DecodeGlueJob decodes an aws:glue/job:Job resource.
// Prices on a DPU-hour usage basis (Jobrun operation).
func DecodeGlueJob(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	workerType := ExtractInput(record.Inputs, "workerType")
	if workerType == "" {
		workerType = "G.1X"
	}
	numberOfWorkers := decimal.NewFromInt(10)
	if v := ExtractInput(record.Inputs, "numberOfWorkers"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			numberOfWorkers = decimal.NewFromFloat(n)
		}
	}

	props := map[string]string{
		"workerType":      workerType,
		"numberOfWorkers": numberOfWorkers.String(),
	}

	entry := glueEntry(record, region, inputsJSON, "Job Run",
		map[string]string{
			"group":     "ETL Job run",
			"operation": "Jobrun",
		}, props)
	return []resources.DecodedResource{entry}
}

// DecodeGlueCrawler decodes an aws:glue/crawler:Crawler resource.
// Prices on a DPU-hour usage basis (CrawlerRun operation).
func DecodeGlueCrawler(record resources.ResourceRecord, region, inputsJSON string) []resources.DecodedResource {
	props := map[string]string{"type": record.Type}
	if v := ExtractInput(record.Inputs, "name"); v != "" {
		props["name"] = v
	}

	entry := glueEntry(record, region, inputsJSON, "Crawler Run",
		map[string]string{
			"group":     "Data catalog crawler run",
			"operation": "CrawlerRun",
		}, props)
	return []resources.DecodedResource{entry}
}
