package grpc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EpochGenerator handles the creation of EpochInfo objects from admission requests.
type EpochGenerator struct{}

// GenerateEpochs creates a list of EpochInfo objects based on the admission request.
// It follows these rules:
// 1. Extract the time range from request.source.partition.time_partition.filter
// 2. If epoch_granularity is specified, slice the time range accordingly
// 3. If not specified, default to 1:1 mapping (would require source introspection in real impl)
// 4. Always align to calendar boundaries (no partial days/hours/etc)
func (eg *EpochGenerator) GenerateEpochs(request *myproto.AdmitDatasetRequest) ([]*myproto.EpochInfo, error) {
	timeRange, err := eg.extractTimeRange(request)
	if err != nil {
		return nil, fmt.Errorf("failed to extract time range: %w", err)
	}
	granularity := eg.determineGranularity(request)
	if granularity == nil {
		return nil, fmt.Errorf("failed to determine epoch granularity")
	}
	dimensionValues := eg.extractDimensionValues(request)
	epochs, err := eg.sliceTimeRange(timeRange, granularity, dimensionValues)
	if err != nil {
		return nil, fmt.Errorf("failed to slice time range: %w", err)
	}
	return epochs, nil
}

// extractTimeRange gets the time range from the request source partition
func (eg *EpochGenerator) extractTimeRange(request *myproto.AdmitDatasetRequest) (*myproto.TimeRangeFilter, error) {
	if request.Source == nil || request.Source.Partition == nil || request.Source.Partition.TimePartition == nil {
		return nil, fmt.Errorf("time partition information is incomplete")
	}
	filter := request.Source.Partition.TimePartition.Filter
	if filter.StartInclusive == nil || filter.EndExclusive == nil {
		return nil, fmt.Errorf("start_inclusive and end_exclusive are required")
	}
	return filter, nil
}

// determineGranularity returns the epoch granularity.
// If request.EpochGranularity is set, use it.
// Otherwise, derive from source partition's source_granularity (1:1 mapping).
func (eg *EpochGenerator) determineGranularity(request *myproto.AdmitDatasetRequest) *myproto.EpochGranularity {
	// If explicitly specified, use it
	if request.EpochGranularity != nil {
		return request.EpochGranularity
	}
	dataSource := request.GetSource()
	if dataSource == nil || dataSource.Partition == nil || dataSource.Partition.TimePartition == nil {
		return nil
	}
	timePartition := dataSource.Partition.TimePartition
	// Otherwise, derive from source partition granularity (1:1 mapping)
	if timePartition.Column != nil && timePartition.Column.SourceGranularity != nil {
		return eg.convertSourceGranularity(*timePartition.Column.SourceGranularity)
	}
	return nil
}

// convertSourceGranularity converts PartitionColumn_Granularity to EpochGranularity
func (eg *EpochGenerator) convertSourceGranularity(sourceGranularity myproto.PartitionColumn_Granularity) *myproto.EpochGranularity {
	switch sourceGranularity {
	case myproto.PartitionColumn_GRANULARITY_HOUR:
		return &myproto.EpochGranularity{
			Value: 1,
			Unit:  myproto.EpochGranularity_HOUR,
		}
	case myproto.PartitionColumn_GRANULARITY_DAY:
		return &myproto.EpochGranularity{
			Value: 1,
			Unit:  myproto.EpochGranularity_DAY,
		}
	case myproto.PartitionColumn_GRANULARITY_WEEK:
		return &myproto.EpochGranularity{
			Value: 1,
			Unit:  myproto.EpochGranularity_WEEK,
		}
	case myproto.PartitionColumn_GRANULARITY_MONTH:
		return &myproto.EpochGranularity{
			Value: 1,
			Unit:  myproto.EpochGranularity_MONTH,
		}
	case myproto.PartitionColumn_GRANULARITY_UNSPECIFIED:
		return nil
	default:
		logger.Info("Unrecognized source_granularity",
			"source_granularity", sourceGranularity)
		return nil
	}
}

// extractDimensionValues builds a map of dimension partition values
func (eg *EpochGenerator) extractDimensionValues(request *myproto.AdmitDatasetRequest) map[string]string {
	dimensionValues := make(map[string]string)
	if request.Source == nil || request.Source.Partition == nil {
		return dimensionValues
	}
	for _, dimPartition := range request.Source.Partition.DimensionPartitions {
		if dimPartition.Column != nil && dimPartition.Values != nil && len(dimPartition.Values.Values) > 0 {
			// For simplicity, use the first value. In a real implementation,
			// you'd create cross-product of all dimension combinations
			dimensionValues[dimPartition.Column.Name] = dimPartition.Values.Values[0]
		}
	}
	return dimensionValues
}

// sliceTimeRange divides the time range into epochs based on the granularity
func (eg *EpochGenerator) sliceTimeRange(
	timeRange *myproto.TimeRangeFilter,
	granularity *myproto.EpochGranularity,
	dimensionValues map[string]string,
) ([]*myproto.EpochInfo, error) {
	start := timeRange.StartInclusive.AsTime()
	end := timeRange.EndExclusive.AsTime()
	if start.After(end) || start.Equal(end) {
		return nil, fmt.Errorf("invalid time range: start must be before end")
	}
	// Step 1: Truncate start to the calendar boundary
	truncatedStart := eg.truncateToGranularity(start, granularity)
	var epochs []*myproto.EpochInfo
	current := truncatedStart
	// Step 2: Iterate and create epochs
	for current.Before(end) {
		epochEnd := eg.addGranularity(current, granularity)
		// Create EpochInfo
		epoch := &myproto.EpochInfo{
			EpochId: eg.generateEpochID(current, dimensionValues),
			TimeRange: &myproto.TimeRange{
				StartInclusive: timestamppb.New(current),
				EndExclusive:   timestamppb.New(epochEnd),
			},
			DimensionValues: dimensionValues,
		}
		epochs = append(epochs, epoch)
		current = epochEnd
	}
	if len(epochs) == 0 {
		return nil, fmt.Errorf("no epochs generated from time range")
	}
	return epochs, nil
}

// truncateToGranularity aligns the timestamp to the calendar boundary
func (eg *EpochGenerator) truncateToGranularity(t time.Time, granularity *myproto.EpochGranularity) time.Time {
	switch granularity.Unit {
	case myproto.EpochGranularity_HOUR:
		return t.Truncate(time.Hour)
	case myproto.EpochGranularity_DAY:
		return eg.truncateToDay(t)
	case myproto.EpochGranularity_WEEK:
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday is 7 in ISO 8601
		}
		daysToSubtract := weekday - 1
		weekStart := t.AddDate(0, 0, -daysToSubtract)
		return eg.truncateToDay(weekStart)
	case myproto.EpochGranularity_MONTH:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	default:
		// Fallback to day for unspecified or unknown units
		return eg.truncateToDay(t)
	}
}

// truncateToDay truncates a time to the beginning of the day (midnight).
func (eg *EpochGenerator) truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// addGranularity adds the specified granularity duration to the timestamp
func (eg *EpochGenerator) addGranularity(t time.Time, granularity *myproto.EpochGranularity) time.Time {
	value := int(granularity.Value)
	switch granularity.Unit {
	case myproto.EpochGranularity_HOUR:
		return t.Add(time.Duration(value) * time.Hour)
	case myproto.EpochGranularity_DAY:
		return t.AddDate(0, 0, value)
	case myproto.EpochGranularity_WEEK:
		return t.AddDate(0, 0, value*7)
	case myproto.EpochGranularity_MONTH:
		return t.AddDate(0, value, 0)
	default:
		panic(fmt.Errorf("unknown granularity unit %d", granularity.Unit))
	}
}

// generateEpochID creates a time-sortable epoch identifier
// Format: {start_timestamp_ms}_{hash_suffix}
// Example: "1730419200000_a7b3c2"
func (eg *EpochGenerator) generateEpochID(start time.Time, dimensionValues map[string]string) string {
	// Convert to milliseconds since epoch
	timestampMs := start.UnixMilli()
	// Create a deterministic hash from timestamp + dimensions
	hasher := sha256.New()
	hasher.Write([]byte(fmt.Sprintf("%d", timestampMs)))
	for key, value := range dimensionValues {
		hasher.Write([]byte(fmt.Sprintf("%s=%s", key, value)))
	}
	hashBytes := hasher.Sum(nil)
	hashSuffix := hex.EncodeToString(hashBytes[:3]) // Use first 3 bytes (6 hex chars)

	return fmt.Sprintf("%d_%s", timestampMs, hashSuffix)
}
