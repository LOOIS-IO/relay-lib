package cloudwatch

import (
	"fmt"
	"github.com/Loopring/relay-lib/log"
	"github.com/Loopring/relay-lib/utils"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatch"
	"net"
	"time"
)

const region = "ap-northeast-1"
const namespace = "LoopringDefine"
const obsoleteCountThreshold = 1000
const obsoleteTimeoutSeconds = 4
const batchDatumBufferSize = 2000
const batchTimeoutSeconds = 2
const batchSendSize = 500

var cwc *cloudwatch.CloudWatch
var inChan chan<- interface{}
var outChan <-chan interface{}

/*
 need config following config files for aws sns service connect
	~/.aws/config/config
	~/.aws/config/credentials
*/

func Initialize() error {
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewSharedCredentials("", ""),
	})
	if err != nil {
		return err
	} else {
		cwc = cloudwatch.New(sess)
		inChan, outChan = utils.MakeInfinite()
		go func() {
			obsoleteTimes := 0
			batchDatumBuffer := make([]*cloudwatch.MetricDatum, 0, batchDatumBufferSize)
			bufferStartTimeStamp := time.Now()
			for {
				select {
				case data, ok := <- outChan:
					if !ok {
						log.Error("Receive from cloud watch output channel failed")
					} else {
						datum, ok := data.(*cloudwatch.MetricDatum)
						if !ok {
							log.Error("Convert data to PutMetricDataInput failed")
						} else {
							if checkObsolete(datum) {
								obsoleteTimes += 1
								if obsoleteTimes >= obsoleteCountThreshold {
									log.Errorf("Obsolete cloud watch metric data count is %d, just drop\n", obsoleteTimes)
									obsoleteTimes = 0
								}
							} else {
								batchDatumBuffer = append(batchDatumBuffer, datum)
								if obsoleteTimes > 0 {
									fmt.Printf("Drop %d obsolete cloud watch metric data\n", obsoleteTimes)
									obsoleteTimes = 0
								}
							}
							if checkTimeout(datum, bufferStartTimeStamp) && len(batchDatumBuffer) > 0 || len(batchDatumBuffer) >= batchDatumBufferSize {
								batchSendMetricData(batchDatumBuffer)
								batchDatumBuffer = make([]*cloudwatch.MetricDatum, 0, batchDatumBufferSize)
								bufferStartTimeStamp = *datum.Timestamp
							}
						}
					}
				}
			}
		}()
		return nil
	}
}

func Close() {
	close(inChan)
}

func IsValid() bool {
	return cwc != nil
}

func PutResponseTimeMetric(methodName string, costTime float64) error {
	if !IsValid() {
		return fmt.Errorf("Cloudwatch client has not initialized\n")
	}
	dt := &cloudwatch.MetricDatum{}
	metricName := fmt.Sprintf("response_%s", methodName)
	dt.MetricName = &metricName
	dt.Dimensions = []*cloudwatch.Dimension{}
	dt.Dimensions = append(dt.Dimensions, hostDimension())
	dt.Value = &costTime
	unit := cloudwatch.StandardUnitMilliseconds
	dt.Unit = &unit
	tms := time.Now()
	dt.Timestamp = &tms

	return storeMetricLocal(dt)
}

func PutHeartBeatMetric(metricName string) error {
	if !IsValid() {
		return fmt.Errorf("Cloudwatch client has not initialized\n")
	}
	res := innerPutHeartBeatMetric(metricName, true)
	if res != nil {
		return res
	}
	res = innerPutHeartBeatMetric(metricName, false)
	return res
}

func innerPutHeartBeatMetric(metricName string, withDimension bool) error {
	dt := &cloudwatch.MetricDatum{}
	dt.MetricName = &metricName
	if withDimension {
		dt.Dimensions = []*cloudwatch.Dimension{}
		dt.Dimensions = append(dt.Dimensions, hostDimension())
	}
	hearbeatValue := 1.0
	dt.Value = &hearbeatValue
	unit := cloudwatch.StandardUnitCount
	dt.Unit = &unit
	tms := time.Now()
	dt.Timestamp = &tms

	return storeMetricLocal(dt)
}

func storeMetricLocal(input *cloudwatch.MetricDatum) error {
	inChan <- input
	return nil
}

func batchSendMetricData(datums []*cloudwatch.MetricDatum) {
	//fmt.Printf("batchSendMetricData %s send datums size %d\n", time.Now().Format(time.RFC3339), len(datums))
	for i := 0;; i++ {
		if i * batchSendSize >= len(datums) {
			return
		}
		input := &cloudwatch.PutMetricDataInput{}
		endIndex := (i+1)*batchSendSize
		if endIndex > len(datums) {
			endIndex = len(datums)
		}
		input.MetricData = datums[i*batchSendSize: endIndex]
		input.Namespace = namespaceNormal()
		go func() {
			cwc.PutMetricData(input)
		}()
	}
}

func checkObsolete(datum *cloudwatch.MetricDatum) bool {
	//fmt.Printf("checkObsolete : %d %d %d \n", time.Now().UnixNano(), datum.Timestamp.UnixNano(), time.Now().UnixNano() - datum.Timestamp.UnixNano())
	return time.Now().UnixNano() - datum.Timestamp.UnixNano() > 1000*1000*1000*obsoleteTimeoutSeconds
}

func checkTimeout(datum *cloudwatch.MetricDatum, startTime time.Time) bool {
	return datum.Timestamp.UnixNano() - startTime.UnixNano() > 1000*1000*1000*batchTimeoutSeconds
}

func namespaceNormal() *string {
	sp := namespace
	return &sp
}

func hostDimension() *cloudwatch.Dimension {
	dim := &cloudwatch.Dimension{}
	ipDimName := "host"
	dim.Name = &ipDimName
	dim.Value = getIp()
	return dim
}

func getIp() *string {
	var res = "unknown"
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return &res
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				res = ipnet.IP.To4().String()
			}
		}
	}
	return &res
}
