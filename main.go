package main

import (
    "github.com/aws/aws-sdk-go/aws"
    "github.com/aws/aws-sdk-go/aws/credentials"
    "github.com/aws/aws-sdk-go/aws/session"
    "github.com/aws/aws-sdk-go/service/route53"
    "github.com/aws/aws-sdk-go/service/cloudwatch"
    "github.com/aws/aws-sdk-go/service/s3"
    "github.com/aws/aws-sdk-go/aws/awserr"
    envconfig "github.com/kelseyhightower/envconfig"
    log "github.com/Sirupsen/logrus"
    "encoding/json"
    "time"
    "bytes"
    "io/ioutil"
)

type ServiceSpec struct {
	Name string
  DisplayName string
  S3DataPath string
  EnvironmentSpecs []EnvironmentSpec `json:"Environments"`
}

type Service struct {
	Name string
  DisplayName string
  Environments []Environment
  Health int
  Reason string
}

type EnvironmentSpec struct {
  Name string
  HostedZoneId string
  DomainName string
}

type Environment struct {
  Name string
  Instances []Instance
  AsOfTime int32
  Health int
  Reason string
}

type Instance struct {
  Name string
  Health int
  Reason string
}

type Specification struct {
  AwsAccessKeyIdFetch string `envconfig:"AWS_ACCESS_KEY_ID_FETCH"`
  AwsSecretAccessKeyFetch string `envconfig:"AWS_SECRET_ACCESS_KEY_FETCH"`
  AwsAccessKeyIdPost string `envconfig:"AWS_ACCESS_KEY_ID_POST"`
  AwsSecretAccessKeyPost string `envconfig:"AWS_SECRET_ACCESS_KEY_POST"`
  ConfigPath string `envconfig:"CONFIG_PATH"`
  AwsDebug bool `envconfig:"AWS_DEBUG"`
  RunInterval int32 `envconfig:"RUN_INTERVAL"`
  S3BucketPost string `json:"S3BucketPost"`
  ServiceSpecs []ServiceSpec `json:"Services"`
}

var Spec Specification
var cw *cloudwatch.CloudWatch
var r53 *route53.Route53
var s3service *s3.S3

func main () {

  log.SetLevel(log.DebugLevel)

  // Parse environment variables
  err := envconfig.Process("", &Spec)
  if err != nil {
      log.Fatal(err.Error())
  }

  // Read config file (Spec)
  config, err := ioutil.ReadFile(Spec.ConfigPath)
  if err != nil {
    log.Fatal("Error loading config file: ", Spec.ConfigPath)
  }
  json.Unmarshal(config, &Spec)

  // Set AWS log level
  awsLogLevel := aws.LogOff
  if (Spec.AwsDebug) {
    awsLogLevel = aws.LogDebugWithHTTPBody
  }

  // Session for pulling status info
  fetchCreds := credentials.NewStaticCredentials(Spec.AwsAccessKeyIdFetch, Spec.AwsSecretAccessKeyFetch, "")
  sessFetch, err := session.NewSession(&aws.Config{Credentials: fetchCreds, Region: aws.String("us-east-1"), LogLevel: aws.LogLevel(awsLogLevel)})

  // Session for pushing status to S3
  postCreds := credentials.NewStaticCredentials(Spec.AwsAccessKeyIdPost, Spec.AwsSecretAccessKeyPost, "")
  sessPost, err := session.NewSession(&aws.Config{Region: aws.String("us-east-1"), Credentials: postCreds, LogLevel: aws.LogLevel(awsLogLevel)})

  if err != nil {
    log.Fatal("Error creating AWS session", err)
  }

  r53 = route53.New(sessFetch)
  cw = cloudwatch.New(sessFetch)
  s3service = s3.New(sessPost)

  tick := time.Tick(time.Duration(Spec.RunInterval) * time.Millisecond)
  run()
  for range tick {
    run()
  }
}

func run () {
  for _, serviceSpec := range Spec.ServiceSpecs {
    getService(&serviceSpec)
  }
}

func getService(serviceSpec *ServiceSpec) {

  service := Service{Name: serviceSpec.Name, DisplayName: serviceSpec.DisplayName}

  for _, environmentSpec := range serviceSpec.EnvironmentSpecs {
    environment := Environment{Name: environmentSpec.Name}
    getEnvironment(&environmentSpec, &environment)
    service.Environments = append(service.Environments, environment)
  }

  output, err := json.Marshal(service)
  if err != nil {
    log.Error("Unable to create JSON output", err)
  }

  postToS3(output, &serviceSpec.S3DataPath)
}

func getEnvironment (environmentSpec *EnvironmentSpec, environment *Environment) {

  listResourceRecordSetsInput := route53.ListResourceRecordSetsInput{HostedZoneId: &environmentSpec.HostedZoneId}
  result, err := r53.ListResourceRecordSets(&listResourceRecordSetsInput)

  if err != nil {
    if aerr, ok := err.(awserr.Error); ok {
			if(aerr.Code() == "Throttling") {
        // Route53 has low throttling thresholds so ignore if being throttled
        log.Warning("ListResourceRecordSets rate throttled")
      } else {
        log.Fatal("Error calling ListResourceRecordSets", err)
      }
    } else {
      log.Fatal("Error calling ListResourceRecordSets", err)
    }
  }

  for _, recordSet := range result.ResourceRecordSets {
    if (aws.StringValue(recordSet.Name) == environmentSpec.DomainName+"." && aws.StringValue(recordSet.Type) == "A") {
      setInstance(environment, recordSet)
    }
  }

  environment.AsOfTime = int32(time.Now().Unix())
}

func setInstance (environment *Environment, recordSet *route53.ResourceRecordSet) {

  instance := Instance{Name: aws.StringValue(recordSet.Region)}
  healthCheckId := aws.StringValue(recordSet.HealthCheckId);

  if(healthCheckId != "") {
    dimensionName := "HealthCheckId"
    metricName := "HealthCheckStatus"
    namespace := "AWS/Route53"
    var dimensions []*cloudwatch.Dimension
    dimensions = append(dimensions, &cloudwatch.Dimension{Name: &dimensionName, Value: &healthCheckId})
    alarm, err := cw.DescribeAlarmsForMetric(&cloudwatch.DescribeAlarmsForMetricInput{Dimensions: dimensions, MetricName: &metricName, Namespace: &namespace})

    if err != nil {
      log.Fatal("Error calling DescribeAlarmsForMetric", err)
    }

    if(len(alarm.MetricAlarms) > 0) {
      if(aws.StringValue(alarm.MetricAlarms[0].StateValue) == "OK") {
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
  } else {
    log.Warn("No Healthcheck found for record set ", aws.StringValue(recordSet.Name), " ", aws.StringValue(recordSet.Region))
    instance.Health = 1
    instance.Reason = "No Healthcheck Found"
  }

  environment.Instances = append(environment.Instances, instance)
}

func postToS3(json []byte, path *string) {

  putObjectInput := s3.PutObjectInput{
      Bucket:               aws.String(Spec.S3BucketPost),
      Key:                  path,
      Body:                 bytes.NewReader(json),
      ContentType:          aws.String("application/json"),
  }

  _, err := s3service.PutObject(&putObjectInput)

  if err != nil {
    if aerr, ok := err.(awserr.Error); ok {
			log.Info(aerr.Code())
    }
    log.Fatal("Error uploading stats to S3; ", err)
  }

  log.Info("Successfully posted data to s3: ", Spec.S3BucketPost, "/", *path)
}
