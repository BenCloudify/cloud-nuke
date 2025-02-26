package aws

import (
	"sync"

	commonTelemetry "github.com/gruntwork-io/go-commons/telemetry"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/apigateway"
	"github.com/gruntwork-io/cloud-nuke/config"
	"github.com/gruntwork-io/cloud-nuke/logging"
	"github.com/gruntwork-io/cloud-nuke/report"
	"github.com/gruntwork-io/cloud-nuke/telemetry"
	"github.com/gruntwork-io/go-commons/errors"
	"github.com/hashicorp/go-multierror"
)

func (gateway ApiGateway) getAll(configObj config.Config) ([]*string, error) {
	result, err := gateway.Client.GetRestApis(&apigateway.GetRestApisInput{})
	if err != nil {
		return []*string{}, errors.WithStackTrace(err)
	}

	var IDs []*string
	for _, api := range result.Items {
		if configObj.APIGateway.ShouldInclude(config.ResourceValue{
			Name: api.Name,
			Time: api.CreatedDate,
		}) {
			IDs = append(IDs, api.Id)
		}
	}

	return IDs, nil
}

func (gateway ApiGateway) nukeAll(identifiers []*string) error {
	if len(identifiers) == 0 {
		logging.Logger.Debugf("No API Gateways (v1) to nuke in region %s", gateway.Region)
	}

	if len(identifiers) > 100 {
		logging.Logger.Errorf("Nuking too many API Gateways (v1) at once (100): " +
			"halting to avoid hitting AWS API rate limiting")
		return TooManyApiGatewayErr{}
	}

	// There is no bulk delete Api Gateway API, so we delete the batch of gateways concurrently using goroutines
	logging.Logger.Debugf("Deleting Api Gateways (v1) in region %s", gateway.Region)
	wg := new(sync.WaitGroup)
	wg.Add(len(identifiers))
	errChans := make([]chan error, len(identifiers))
	for i, apigwID := range identifiers {
		errChans[i] = make(chan error, 1)
		go gateway.nukeAsync(wg, errChans[i], apigwID)
	}
	wg.Wait()

	var allErrs *multierror.Error
	for _, errChan := range errChans {
		if err := <-errChan; err != nil {
			allErrs = multierror.Append(allErrs, err)
			logging.Logger.Debugf("[Failed] %s", err)

			telemetry.TrackEvent(commonTelemetry.EventContext{
				EventName: "Error Nuking API Gateway",
			}, map[string]interface{}{
				"region": gateway.Region,
			})
		}
	}
	finalErr := allErrs.ErrorOrNil()
	if finalErr != nil {
		return errors.WithStackTrace(finalErr)
	}

	return nil
}

func (gateway ApiGateway) nukeAsync(
	wg *sync.WaitGroup, errChan chan error, apigwID *string) {
	defer wg.Done()

	input := &apigateway.DeleteRestApiInput{RestApiId: apigwID}
	_, err := gateway.Client.DeleteRestApi(input)
	errChan <- err

	// Record status of this resource
	e := report.Entry{
		Identifier:   *apigwID,
		ResourceType: "APIGateway (v1)",
		Error:        err,
	}
	report.Record(e)

	if err == nil {
		logging.Logger.Debugf("["+
			"OK] API Gateway (v1) %s deleted in %s", aws.StringValue(apigwID), gateway.Region)
		return
	}

	logging.Logger.Debugf(
		"[Failed] Error deleting API Gateway (v1) %s in %s", aws.StringValue(apigwID), gateway.Region)
}
