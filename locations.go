package pinesandbox

import "context"

// AvailableLocations is the project-visible country catalog at read time.
// Locations contains inventory-backed choices. DefaultLocation is the
// server-owned choice when create/attach omits Location and can temporarily be
// absent from Locations during an inventory incident.
type AvailableLocations struct {
	Locations       []ComputerLocation
	DefaultLocation ComputerLocation
}

// AvailableLocations discovers country intents without exposing provider,
// pool, allocation, or runtime-health details.
func (c *Client) AvailableLocations(ctx context.Context) (*AvailableLocations, error) {
	result, err := c.conn.attachProvider.AvailableLocations(ctx)
	if err != nil {
		return nil, err
	}
	locations := make([]ComputerLocation, len(result.Locations))
	for i, location := range result.Locations {
		locations[i] = ComputerLocation{Country: location.Country}
	}
	return &AvailableLocations{
		Locations: locations,
		DefaultLocation: ComputerLocation{
			Country: result.DefaultLocation.Country,
		},
	}, nil
}
