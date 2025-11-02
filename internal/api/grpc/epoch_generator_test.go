package grpc

import (
	"testing"
	"time"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestEpochGenerator_GenerateEpochs_DailyGranularity(t *testing.T) {
	generator := &EpochGenerator{}
	start := time.Date(2023, 10, 25, 8, 30, 0, 0, time.UTC)
	end := time.Date(2023, 10, 28, 10, 0, 0, 0, time.UTC)

	request := &myproto.AdmitDatasetRequest{
		TenantId:  "tenant-123",
		DatasetId: "dataset-456",
		Source: &myproto.DataSource{
			Partition: &myproto.Partition{
				TimePartition: &myproto.TimePartition{
					Filter: &myproto.TimeRangeFilter{
						StartInclusive: timestamppb.New(start),
						EndExclusive:   timestamppb.New(end),
					},
				},
			},
		},
		// EpochGranularity not set - should default to 1 DAY
	}

	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		t.Fatalf("GenerateEpochs failed: %v", err)
	}

	// Verify we get 4 epochs
	if len(epochs) != 4 {
		t.Errorf("Expected 4 epochs, got %d", len(epochs))
	}

	// Verify first epoch starts at midnight
	firstEpochStart := epochs[0].TimeRange.StartInclusive.AsTime()
	expectedStart := time.Date(2023, 10, 25, 0, 0, 0, 0, time.UTC)
	if !firstEpochStart.Equal(expectedStart) {
		t.Errorf("First epoch start: expected %v, got %v", expectedStart, firstEpochStart)
	}

	// Verify last epoch ends at the next day boundary after original end
	lastEpochEnd := epochs[len(epochs)-1].TimeRange.EndExclusive.AsTime()
	expectedEnd := time.Date(2023, 10, 29, 0, 0, 0, 0, time.UTC)
	if !lastEpochEnd.Equal(expectedEnd) {
		t.Errorf("Last epoch end: expected %v, got %v", expectedEnd, lastEpochEnd)
	}

	// Verify epoch IDs are properly formatted
	for i, epoch := range epochs {
		if epoch.EpochId == "" {
			t.Errorf("Epoch %d has empty ID", i)
		}
		t.Logf("Epoch %d: ID=%s, Start=%v, End=%v",
			i,
			epoch.EpochId,
			epoch.TimeRange.StartInclusive.AsTime(),
			epoch.TimeRange.EndExclusive.AsTime())
	}
}

func TestEpochGenerator_GenerateEpochs_3DayGranularity(t *testing.T) {
	generator := &EpochGenerator{}

	// Test case: 10 days with 3-day granularity
	// Start: 2023-10-01 00:00:00
	// End: 2023-10-11 00:00:00
	// Expected: 4 epochs (Oct 1-4, 4-7, 7-10, 10-13)
	start := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2023, 10, 11, 0, 0, 0, 0, time.UTC)

	request := &myproto.AdmitDatasetRequest{
		TenantId:  "tenant-123",
		DatasetId: "dataset-456",
		Source: &myproto.DataSource{
			Partition: &myproto.Partition{
				TimePartition: &myproto.TimePartition{
					Filter: &myproto.TimeRangeFilter{
						StartInclusive: timestamppb.New(start),
						EndExclusive:   timestamppb.New(end),
					},
				},
			},
		},
		EpochGranularity: &myproto.EpochGranularity{
			Value: 3,
			Unit:  myproto.EpochGranularity_DAY,
		},
	}

	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		t.Fatalf("GenerateEpochs failed: %v", err)
	}

	// Verify we get 4 epochs (to cover 10 days with 3-day chunks)
	if len(epochs) != 4 {
		t.Errorf("Expected 4 epochs, got %d", len(epochs))
	}

	// Verify each epoch spans exactly 3 days
	for i, epoch := range epochs {
		epochStart := epoch.TimeRange.StartInclusive.AsTime()
		epochEnd := epoch.TimeRange.EndExclusive.AsTime()
		duration := epochEnd.Sub(epochStart)
		expectedDuration := 3 * 24 * time.Hour

		if duration != expectedDuration {
			t.Errorf("Epoch %d duration: expected %v, got %v", i, expectedDuration, duration)
		}

		t.Logf("Epoch %d: ID=%s, Start=%v, End=%v, Duration=%v",
			i,
			epoch.EpochId,
			epochStart,
			epochEnd,
			duration)
	}
}

func TestEpochGenerator_GenerateEpochs_HourlyGranularity(t *testing.T) {
	generator := &EpochGenerator{}
	// Test case: 5 hours with hourly granularity
	// Start: 2023-10-25 14:30:00
	// End: 2023-10-25 19:15:00
	// Expected: 5 epochs (14:00-15:00, 15:00-16:00, 16:00-17:00, 17:00-18:00, 18:00-19:00)
	start := time.Date(2023, 10, 25, 14, 30, 0, 0, time.UTC)
	end := time.Date(2023, 10, 25, 19, 15, 0, 0, time.UTC)
	request := &myproto.AdmitDatasetRequest{
		TenantId:  "tenant-123",
		DatasetId: "dataset-456",
		Source: &myproto.DataSource{
			Partition: &myproto.Partition{
				TimePartition: &myproto.TimePartition{
					Filter: &myproto.TimeRangeFilter{
						StartInclusive: timestamppb.New(start),
						EndExclusive:   timestamppb.New(end),
					},
				},
			},
		},
		EpochGranularity: &myproto.EpochGranularity{
			Value: 1,
			Unit:  myproto.EpochGranularity_HOUR,
		},
	}

	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		t.Fatalf("GenerateEpochs failed: %v", err)
	}

	// Verify we get 6 epochs (to cover 14:30 to 19:15 with hourly chunks, truncated to 14:00)
	if len(epochs) != 6 {
		t.Errorf("Expected 6 epochs, got %d", len(epochs))
	}

	// Verify first epoch starts at 14:00 (truncated from 14:30)
	firstEpochStart := epochs[0].TimeRange.StartInclusive.AsTime()
	expectedStart := time.Date(2023, 10, 25, 14, 0, 0, 0, time.UTC)
	if !firstEpochStart.Equal(expectedStart) {
		t.Errorf("First epoch start: expected %v, got %v", expectedStart, firstEpochStart)
	}

	for i, epoch := range epochs {
		t.Logf("Epoch %d: ID=%s, Start=%v, End=%v",
			i,
			epoch.EpochId,
			epoch.TimeRange.StartInclusive.AsTime(),
			epoch.TimeRange.EndExclusive.AsTime())
	}
}

func TestEpochGenerator_GenerateEpochs_WithDimensions(t *testing.T) {
	generator := &EpochGenerator{}

	start := time.Date(2023, 10, 25, 0, 0, 0, 0, time.UTC)
	end := time.Date(2023, 10, 26, 0, 0, 0, 0, time.UTC)

	request := &myproto.AdmitDatasetRequest{
		TenantId:  "tenant-123",
		DatasetId: "dataset-456",
		Source: &myproto.DataSource{
			Partition: &myproto.Partition{
				TimePartition: &myproto.TimePartition{
					Filter: &myproto.TimeRangeFilter{
						StartInclusive: timestamppb.New(start),
						EndExclusive:   timestamppb.New(end),
					},
				},
				DimensionPartitions: []*myproto.DimensionPartition{
					{
						Column: &myproto.PartitionColumn{
							Name: "country",
							Type: myproto.PartitionColumn_STRING,
						},
						Values: &myproto.PartitionValues{
							Values: []string{"US"},
						},
					},
					{
						Column: &myproto.PartitionColumn{
							Name: "region",
							Type: myproto.PartitionColumn_STRING,
						},
						Values: &myproto.PartitionValues{
							Values: []string{"west"},
						},
					},
				},
			},
		},
	}

	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		t.Fatalf("GenerateEpochs failed: %v", err)
	}

	// Verify dimension values are included
	if len(epochs) == 0 {
		t.Fatal("Expected at least one epoch")
	}

	epoch := epochs[0]
	if epoch.DimensionValues["country"] != "US" {
		t.Errorf("Expected country=US, got %s", epoch.DimensionValues["country"])
	}
	if epoch.DimensionValues["region"] != "west" {
		t.Errorf("Expected region=west, got %s", epoch.DimensionValues["region"])
	}

	t.Logf("Epoch ID with dimensions: %s", epoch.EpochId)
	t.Logf("Dimension values: %v", epoch.DimensionValues)
}

func TestEpochGenerator_GenerateEpochs_FromSourceGranularity(t *testing.T) {
	generator := &EpochGenerator{}

	// Test case: source_granularity is HOUR - should create hourly epochs
	// Start: 2023-10-25 14:30:00
	// End: 2023-10-25 17:15:00
	// Expected: hourly epochs based on source_granularity
	start := time.Date(2023, 10, 25, 14, 30, 0, 0, time.UTC)
	end := time.Date(2023, 10, 25, 17, 15, 0, 0, time.UTC)

	sourceGranularity := myproto.PartitionColumn_GRANULARITY_HOUR
	request := &myproto.AdmitDatasetRequest{
		TenantId:  "tenant-123",
		DatasetId: "dataset-456",
		Source: &myproto.DataSource{
			Partition: &myproto.Partition{
				TimePartition: &myproto.TimePartition{
					Column: &myproto.PartitionColumn{
						Name:              "event_time",
						Type:              myproto.PartitionColumn_TIMESTAMP,
						SourceGranularity: &sourceGranularity,
					},
					Filter: &myproto.TimeRangeFilter{
						StartInclusive: timestamppb.New(start),
						EndExclusive:   timestamppb.New(end),
					},
				},
			},
		},
		// EpochGranularity not set - should derive from source_granularity
	}

	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		t.Fatalf("GenerateEpochs failed: %v", err)
	}

	// Should generate hourly epochs (truncated to 14:00)
	if len(epochs) != 4 {
		t.Errorf("Expected 4 hourly epochs, got %d", len(epochs))
	}

	// Verify first epoch starts at 14:00
	firstEpochStart := epochs[0].TimeRange.StartInclusive.AsTime()
	expectedStart := time.Date(2023, 10, 25, 14, 0, 0, 0, time.UTC)
	if !firstEpochStart.Equal(expectedStart) {
		t.Errorf("First epoch start: expected %v, got %v", expectedStart, firstEpochStart)
	}

	// Verify each epoch is 1 hour
	for i, epoch := range epochs {
		duration := epoch.TimeRange.EndExclusive.AsTime().Sub(epoch.TimeRange.StartInclusive.AsTime())
		if duration != time.Hour {
			t.Errorf("Epoch %d: expected 1 hour duration, got %v", i, duration)
		}
		t.Logf("Epoch %d: ID=%s, Start=%v, End=%v",
			i,
			epoch.EpochId,
			epoch.TimeRange.StartInclusive.AsTime(),
			epoch.TimeRange.EndExclusive.AsTime())
	}
}

func TestEpochGenerator_ConvertSourceGranularity(t *testing.T) {
	generator := &EpochGenerator{}

	tests := []struct {
		name         string
		input        myproto.PartitionColumn_Granularity
		expectedUnit myproto.EpochGranularity_TimeUnit
		expectNil    bool
	}{
		{
			name:         "HOUR",
			input:        myproto.PartitionColumn_GRANULARITY_HOUR,
			expectedUnit: myproto.EpochGranularity_HOUR,
		},
		{
			name:         "DAY",
			input:        myproto.PartitionColumn_GRANULARITY_DAY,
			expectedUnit: myproto.EpochGranularity_DAY,
		},
		{
			name:         "WEEK",
			input:        myproto.PartitionColumn_GRANULARITY_WEEK,
			expectedUnit: myproto.EpochGranularity_WEEK,
		},
		{
			name:         "MONTH",
			input:        myproto.PartitionColumn_GRANULARITY_MONTH,
			expectedUnit: myproto.EpochGranularity_MONTH,
		},
		{
			name:      "UNSPECIFIED",
			input:     myproto.PartitionColumn_GRANULARITY_UNSPECIFIED,
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generator.convertSourceGranularity(tt.input)
			if tt.expectNil {
				if result != nil {
					t.Errorf("convertSourceGranularity(%v): expected nil, got %v", tt.input, result)
				}
				return
			}

			if result == nil {
				t.Fatalf("convertSourceGranularity(%v): expected non-nil result", tt.input)
			}

			if result.Unit != tt.expectedUnit {
				t.Errorf("convertSourceGranularity(%v): expected unit %v, got %v",
					tt.input, tt.expectedUnit, result.Unit)
			}
			if result.Value != 1 {
				t.Errorf("convertSourceGranularity(%v): expected value 1, got %d",
					tt.input, result.Value)
			}
		})
	}
}

func TestEpochGenerator_TruncateToGranularity(t *testing.T) {
	generator := &EpochGenerator{}

	// Test truncation for different granularities
	testTime := time.Date(2023, 10, 25, 14, 35, 42, 123456789, time.UTC)

	tests := []struct {
		name        string
		granularity *myproto.EpochGranularity
		expected    time.Time
	}{
		{
			name: "Hourly truncation",
			granularity: &myproto.EpochGranularity{
				Value: 1,
				Unit:  myproto.EpochGranularity_HOUR,
			},
			expected: time.Date(2023, 10, 25, 14, 0, 0, 0, time.UTC),
		},
		{
			name: "Daily truncation",
			granularity: &myproto.EpochGranularity{
				Value: 1,
				Unit:  myproto.EpochGranularity_DAY,
			},
			expected: time.Date(2023, 10, 25, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "Monthly truncation",
			granularity: &myproto.EpochGranularity{
				Value: 1,
				Unit:  myproto.EpochGranularity_MONTH,
			},
			expected: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generator.truncateToGranularity(testTime, tt.granularity)
			if !result.Equal(tt.expected) {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}
