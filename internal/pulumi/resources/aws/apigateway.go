package aws

import (
	"strings"

	"github.com/OverloadBlitz/cloudcent-cli/internal/pulumi/resources"
)

// AddAPIGatewayV2ProtocolType price for pulumi: aws:apigatewayv2/api:Api and aws:apigateway/restApi:RestApi
// drawio: mxgraph.aws2.app_services.api_gateway, mxgraph.aws3.api_gateway and mxgraph.aws4.api_gateway
func AddAPIGatewayV2ProtocolType(record resources.ResourceRecord, attrs, props map[string]string) {
	protocolType := ExtractInput(record.Inputs, "protocolType")
	if protocolType == "" {
		return
	}

	upper := strings.ToUpper(protocolType)

	if _, alreadyMapped := attrs["protocolType"]; !alreadyMapped {
		props["protocol_type"] = upper
	}

	if upper == "WEBSOCKET" {
		attrs["productFamily"] = "WebSocket"
		props["productFamily"] = "WebSocket"
		attrs["operation"] = "ApiGatewayWebSocket"
		props["operation"] = "ApiGatewayWebSocket"
	}
}
