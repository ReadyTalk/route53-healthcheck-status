package main

import (
  "bytes"
  "encoding/json"
  "io/ioutil"
  "time"
  "os"

  log "github.com/Sirupsen/logrus"
  "github.com/aws/aws-sdk-go/aws"
  "github.com/aws/aws-sdk-go/aws/awserr"
  "github.com/aws/aws-sdk-go/aws/credentials"
  "github.com/aws/aws-sdk-go/aws/session"
  "github.com/aws/aws-sdk-go/service/cloudwatch"
  "github.com/aws/aws-sdk-go/service/route53"
  "github.com/aws/aws-sdk-go/service/s3"
  envconfig "github.com/kelseyhightower/envconfig"
)

type ServiceSpec struct {
  Name             string
  DisplayName      string
  S3DataPath       string
  EnvironmentSpecs []EnvironmentSpec `json:"Environments"`
}

type Service struct {
  Name         string
  DisplayName  string
  Environments []Environment
}

type EnvironmentSpec struct {
  Name         string
  HostedZoneId string
  DomainName   string
}

type Environment struct {
  Name      string
  Instances []Instance
  AsOfTime  int32
  Health    int
  Reason    string
}

type Instance struct {
  Name   string
  Health int
  Reason string
}


type EnvConfig struct {
  AwsAccessKeyIdFetch     string `envconfig:"AWS_ACCESS_KEY_ID_FETCH"`
  AwsSecretAccessKeyFetch string `envconfig:"AWS_SECRET_ACCESS_KEY_FETCH"`
  AwsAccessKeyIdPost      string `envconfig:"AWS_ACCESS_KEY_ID_POST"`
  AwsSecretAccessKeyPost  string `envconfig:"AWS_SECRET_ACCESS_KEY_POST"`
  ConfigPath              string `envconfig:"CONFIG_PATH"`
  AwsDebug                bool   `envconfig:"AWS_DEBUG"`
  PostIntervalSec         int32  `envconfig:"POST_INTERVAL_SEC" default:"30"`
  Route53IntervalSec      int32  `envconfig:"ROUTE53_INTERVAL_SEC" default:"30"`
}

type ServiceConfig struct {
  S3BucketPost string        `json:"S3BucketPost"`
  S3MainPath   string        `json:S3MainPath`
  ServiceSpecs []ServiceSpec `json:"Services"`
}

type HealthCheck struct {
  Health int
  Reason string
}

var CONFIG EnvConfig
var SERVICE_CONFIG ServiceConfig
var cw *cloudwatch.CloudWatch
var r53 *route53.Route53
var s3service *s3.S3
var healthChecks map[string]HealthCheck
var cachedHostedZones map[string][]*route53.ResourceRecordSet
var services []Service

func main() {

  log.SetLevel(log.DebugLevel)
  log.SetOutput(os.Stdout)

  // Parse environment variables
  err := envconfig.Process("", &CONFIG)
  if err != nil {
    log.Fatal(err.Error())
  }
  if CONFIG.Route53IntervalSec < 10 {
    log.Error("Route53 interval must be at least 10 second, setting to 10")
    CONFIG.Route53IntervalSec = 10;
  }
  if CONFIG.PostIntervalSec < 10 {
    log.Error("Post interval must be at least 10 second, setting to 10")
    CONFIG.PostIntervalSec = 10;
  }


  // Read config file
  config, err := ioutil.ReadFile(CONFIG.ConfigPath)
  if err != nil {
    log.Fatal("Error loading config file: ", CONFIG.ConfigPath)
  }
  json.Unmarshal(config, &SERVICE_CONFIG)

  // Set AWS log level
  awsLogLevel := aws.LogOff
  if CONFIG.AwsDebug {
    awsLogLevel = aws.LogDebugWithHTTPBody
  }

  // Session for pulling status info
  fetchCreds := credentials.NewStaticCredentials(CONFIG.AwsAccessKeyIdFetch, CONFIG.AwsSecretAccessKeyFetch, "")
  sessFetch, err := session.NewSession(&aws.Config{Credentials: fetchCreds, Region: aws.String("us-east-1"), LogLevel: aws.LogLevel(awsLogLevel)})

  // Session for pushing status to S3
  postCreds := credentials.NewStaticCredentials(CONFIG.AwsAccessKeyIdPost, CONFIG.AwsSecretAccessKeyPost, "")
  sessPost, err := session.NewSession(&aws.Config{Region: aws.String("us-east-1"), Credentials: postCreds, LogLevel: aws.LogLevel(awsLogLevel)})

  if err != nil {
    log.Fatal("Error creating AWS session", err)
  }

  r53 = route53.New(sessFetch)
  cw = cloudwatch.New(sessFetch)
  s3service = s3.New(sessPost)

  go checkRoute53()
  time.Sleep(time.Duration(5)*time.Second)
  run()
}

func checkRoute53() {
  sleepInt := time.Duration(CONFIG.Route53IntervalSec) * time.Second
  for {
    localHostedZones := make(map[string][]*route53.ResourceRecordSet)
    for _, serviceSpec := range SERVICE_CONFIG.ServiceSpecs {
      for _, envSpec := range serviceSpec.EnvironmentSpecs {
        if _, ok := localHostedZones[envSpec.HostedZoneId]; !ok {
          records, err := fetchHostedZone(envSpec.HostedZoneId)
          if err == nil {
            localHostedZones[envSpec.HostedZoneId] = records
          }
        }
      }
    }
    cachedHostedZones = localHostedZones
    time.Sleep(sleepInt)
  }
}

func run() {
  sleepInt := time.Duration(CONFIG.PostIntervalSec) * time.Second
  for {
    if len(cachedHostedZones) > 0 {
      var services = make(map[string]Service)

      for _, serviceSpec := range SERVICE_CONFIG.ServiceSpecs {
        healthChecks = make(map[string]HealthCheck)
        log.Debug("ServiceSpec.Name: ", serviceSpec.Name)
        services[serviceSpec.Name] = getService(&serviceSpec)
      }

      output, err := json.Marshal(services)
      if err != nil {
        log.Error("Unable to create JSON output", err)
      }
      go postToS3(output)
    } else {
      log.Error("Not updating Json, No host routes found!")
    }
    time.Sleep(sleepInt)
  }
}

func getService(serviceSpec *ServiceSpec) Service {
  service := Service{Name: serviceSpec.Name, DisplayName: serviceSpec.DisplayName}
  for _, environmentSpec := range serviceSpec.EnvironmentSpecs {
    environment := Environment{Name: environmentSpec.Name, Health: 3, Reason: "No Health Status Found"}
    getEnvironment(&environmentSpec, &environment)
    service.Environments = append(service.Environments, environment)
  }
  return service
}

func getEnvironment(environmentSpec *EnvironmentSpec, environment *Environment) {

  records := cachedHostedZones[environmentSpec.HostedZoneId]
  for _, recordSet := range records {
    if aws.StringValue(recordSet.Name) == environmentSpec.DomainName+"." && aws.StringValue(recordSet.Type) == "A" {
      setInstance(environment, recordSet)
    }
  }
  environment.AsOfTime = int32(time.Now().Unix())
}

// Fetches all recordsets from hosted zone either from AWS or from local cache
// Caches due to AWS limits on Route53 API requests
// Returns pointer to all recordsets
func fetchHostedZone(hostedZoneId string) (records []*route53.ResourceRecordSet, err error) {

  log.Debug("Hosted zone ", hostedZoneId, "; Making call to Route53")
  listResourceRecordSetsInput := route53.ListResourceRecordSetsInput{HostedZoneId: &hostedZoneId}
  result, err := r53.ListResourceRecordSets(&listResourceRecordSetsInput)

  if err != nil {
    if aerr, ok := err.(awserr.Error); ok {
      if aerr.Code() == "Throttling" {
        // Route53 has low throttling thresholds so ignore if being throttled
        log.Warning("ListResourceRecordSets rate throttled")
      } else {
        log.Warning("Error calling ListResourceRecordSets", err)
        return nil, err
      }
    } else {
      log.Warning("Error calling ListResourceRecordSets", err)
      return nil, err
    }
  }
  return result.ResourceRecordSets, nil
}

func setInstance(environment *Environment, recordSet *route53.ResourceRecordSet) {

  instance := Instance{Name: aws.StringValue(recordSet.Region)}
  healthCheckId := aws.StringValue(recordSet.HealthCheckId)

  if healthCheckId != "" {

    // If we already checked this healthcheck's alarm, just use that value
    if _, ok := healthChecks[healthCheckId]; ok {
      instance.Health = healthChecks[healthCheckId].Health
      instance.Reason = healthChecks[healthCheckId].Reason
    } else {
      dimensionName := "HealthCheckId"
      metricName := "HealthCheckStatus"
      namespace := "AWS/Route53"
      var dimensions []*cloudwatch.Dimension
      dimensions = append(dimensions, &cloudwatch.Dimension{Name: &dimensionName, Value: &healthCheckId})
      alarm, err := cw.DescribeAlarmsForMetric(&cloudwatch.DescribeAlarmsForMetricInput{Dimensions: dimensions, MetricName: &metricName, Namespace: &namespace})
      if err != nil {
        log.Fatal("Error calling DescribeAlarmsForMetric", err)
      }

      if len(alarm.MetricAlarms) > 0 {
        if aws.StringValue(alarm.MetricAlarms[0].StateValue) == "OK" {
          instance.Health = 0
          instance.Reason = ""
        } else {
          instance.Health = 2
          instance.Reason = "Healthcheck Failing"
        }
      } else {
        log.Warn("No Alarm found for healthCheckId ", healthCheckId)
        instance.Health = 1
        instance.Reason = "No Alarm Found"
      }

      // Add the healthcheck result to the list so we don't have to check it again on this run
      healthChecks[healthCheckId] = HealthCheck{Health: instance.Health, Reason: instance.Reason}
    }
  } else {
    log.Warn("No Healthcheck found for record set ", aws.StringValue(recordSet.Name), " ", aws.StringValue(recordSet.Region))
    instance.Health = 1
    instance.Reason = "No Healthcheck Found"
  }

  if instance.Health < environment.Health {
    environment.Health = instance.Health
    environment.Reason = instance.Reason
  }

  environment.Instances = append(environment.Instances, instance)
}

func postToS3(json []byte) {

  putObjectInput := s3.PutObjectInput{
    Bucket:      aws.String(SERVICE_CONFIG.S3BucketPost),
    Key:         &SERVICE_CONFIG.S3MainPath,
    Body:        bytes.NewReader(json),
    ContentType: aws.String("application/json"),
  }

  _, err := s3service.PutObject(&putObjectInput)

  if err != nil {
    if aerr, ok := err.(awserr.Error); ok {
      log.Info(aerr.Code())
    }
    log.Fatal("Error uploading stats to S3; ", err)
  }

  log.Info("Successfully posted data to s3: ", SERVICE_CONFIG.S3BucketPost, "/", SERVICE_CONFIG.S3MainPath)
}

