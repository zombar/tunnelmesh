// NodeMap - Displays peer locations on a Leaflet map with dark theme
// Location data comes from peer.location object which contains:
// - latitude, longitude: coordinates
// - source: "manual" (user configured) or "ip" (IP geolocation)
// - accuracy: meters (~0 for manual, ~50000 for IP)
// - city, region, country: location details
class NodeMap {
    constructor(containerId) {
        this.containerId = containerId;
        this.map = null;
        this.markers = new Map(); // peerName -> { marker, circle }
        this.bounds = null;
        this.initialized = false;
    }

    // Initialize the Leaflet map with dark theme tiles
    init() {
        if (this.initialized) return;

        const container = document.getElementById(this.containerId);
        if (!container) {
            console.error('Map container not found:', this.containerId);
            return;
        }

        // Initialize map centered on (0, 0) with zoom 2
        this.map = L.map(this.containerId, {
            center: [20, 0],
            zoom: 2,
            scrollWheelZoom: true,
            attributionControl: true
        });

        // Use CartoDB Dark Matter tiles for dark theme
        L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
            attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a> &copy; <a href="https://carto.com/attributions">CARTO</a>',
            subdomains: 'abcd',
            maxZoom: 19
        }).addTo(this.map);

        this.initialized = true;
    }

    // Update markers from peer data
    updatePeers(peers) {
        if (!this.initialized) {
            this.init();
        }

        if (!this.map) return;

        // Track which peers we've processed
        const seenPeers = new Set();
        let hasLocations = false;
        const boundsArray = [];

        peers.forEach(peer => {
            if (!peer.location || peer.location.latitude === 0 && peer.location.longitude === 0) {
                // No valid location - remove marker if exists
                if (this.markers.has(peer.name)) {
                    this.removeMarker(peer.name);
                }
                return;
            }

            hasLocations = true;
            seenPeers.add(peer.name);

            const loc = peer.location;
            const lat = loc.latitude;
            const lng = loc.longitude;
            boundsArray.push([lat, lng]);

            // Determine marker color based on online status and source
            let color;
            if (!peer.online) {
                color = '#6b7280'; // grey for offline
            } else if (loc.source === 'manual') {
                color = '#3fb950'; // green for manual config
            } else {
                color = '#58a6ff'; // blue for IP geolocation
            }

            // Create or update marker
            if (this.markers.has(peer.name)) {
                this.updateMarker(peer.name, lat, lng, color, peer, loc);
            } else {
                this.createMarker(peer.name, lat, lng, color, peer, loc);
            }
        });

        // Remove markers for peers no longer present
        this.markers.forEach((_, peerName) => {
            if (!seenPeers.has(peerName)) {
                this.removeMarker(peerName);
            }
        });

        // Show/hide map section based on whether any peers have locations
        const mapSection = document.getElementById('map-section');
        if (mapSection) {
            const wasHidden = mapSection.style.display === 'none';
            mapSection.style.display = hasLocations ? 'block' : 'none';

            // If map just became visible, invalidate size so Leaflet recalculates
            if (wasHidden && hasLocations && this.map) {
                // Small delay to let the DOM update
                setTimeout(() => {
                    this.map.invalidateSize();
                }, 100);
            }
        }

        // Fit map to show all markers
        if (boundsArray.length > 0 && this.map) {
            const bounds = L.latLngBounds(boundsArray);
            // Only fit bounds if they've changed significantly
            if (!this.bounds || !this.bounds.equals(bounds)) {
                // Delay fitBounds if map was just shown to ensure invalidateSize completed
                const fitBoundsDelay = (mapSection && mapSection.style.display !== 'none') ? 150 : 0;
                setTimeout(() => {
                    if (this.map) {
                        this.map.fitBounds(bounds, { padding: [50, 50], maxZoom: 10 });
                    }
                }, fitBoundsDelay);
                this.bounds = bounds;
            }
        }
    }

    createMarker(name, lat, lng, color, peer, loc) {
        // Create circular marker
        const marker = L.circleMarker([lat, lng], {
            radius: 8,
            fillColor: color,
            color: color,
            weight: 2,
            opacity: 1,
            fillOpacity: 0.8
        }).addTo(this.map);

        // Create accuracy circle for IP geolocation (only if accuracy > 1000m)
        let circle = null;
        if (loc.source === 'ip' && loc.accuracy > 1000 && peer.online) {
            circle = L.circle([lat, lng], {
                radius: loc.accuracy,
                fillColor: color,
                color: color,
                weight: 1,
                opacity: 0.3,
                fillOpacity: 0.1,
                dashArray: '5, 5'
            }).addTo(this.map);
        }

        this.markers.set(name, { marker, circle });
    }

    updateMarker(name, lat, lng, color, peer, loc) {
        const entry = this.markers.get(name);
        if (!entry) return;

        const { marker, circle } = entry;

        // Update position
        marker.setLatLng([lat, lng]);

        // Update style
        marker.setStyle({
            fillColor: color,
            color: color
        });

        // Update or remove accuracy circle
        if (loc.source === 'ip' && loc.accuracy > 1000 && peer.online) {
            if (circle) {
                circle.setLatLng([lat, lng]);
                circle.setRadius(loc.accuracy);
                circle.setStyle({ fillColor: color, color: color });
            } else {
                // Create new circle
                const newCircle = L.circle([lat, lng], {
                    radius: loc.accuracy,
                    fillColor: color,
                    color: color,
                    weight: 1,
                    opacity: 0.3,
                    fillOpacity: 0.1,
                    dashArray: '5, 5'
                }).addTo(this.map);
                entry.circle = newCircle;
            }
        } else if (circle) {
            // Remove circle if no longer needed
            this.map.removeLayer(circle);
            entry.circle = null;
        }
    }

    removeMarker(name) {
        const entry = this.markers.get(name);
        if (!entry) return;

        if (entry.marker) this.map.removeLayer(entry.marker);
        if (entry.circle) this.map.removeLayer(entry.circle);

        this.markers.delete(name);
    }

    // Force map to recalculate size (call after container becomes visible)
    invalidateSize() {
        if (this.map) {
            this.map.invalidateSize();
        }
    }

    // Cleanup
    destroy() {
        if (this.map) {
            this.map.remove();
            this.map = null;
        }
        this.markers.clear();
        this.initialized = false;
    }
}
