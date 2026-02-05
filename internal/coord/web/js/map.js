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
        this.connections = new Map(); // "peerA-peerB" -> polyline
        this.bounds = null;
        this.initialized = false;
        this.selectedPeer = null;
        this.onlinePeersWithLocation = new Map(); // peerName -> {lat, lng}
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

        // Clear online peers tracking for this update
        this.onlinePeersWithLocation.clear();

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

            // Track online peers with location for connection drawing
            if (peer.online) {
                this.onlinePeersWithLocation.set(peer.name, { lat, lng });
            }

            // Determine marker color based on selection, online status and source
            let color;
            if (peer.name === this.selectedPeer) {
                color = '#58a6ff'; // blue for selected
            } else if (!peer.online) {
                color = '#6b7280'; // grey for offline
            } else {
                color = '#3fb950'; // green for online
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

        // Update connections between online peers
        this.updateConnections();

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

    // Update connection lines from selected peer to other online peers
    updateConnections() {
        if (!this.map) return;

        // Build set of expected connection keys (only from selected peer)
        const expectedConnections = new Set();

        // Only draw connections if we have a selected peer that's online with location
        if (this.selectedPeer && this.onlinePeersWithLocation.has(this.selectedPeer)) {
            const selectedLoc = this.onlinePeersWithLocation.get(this.selectedPeer);

            // Create connections from selected peer to all other online peers
            this.onlinePeersWithLocation.forEach((loc, peerName) => {
                if (peerName === this.selectedPeer) return;

                const key = `${this.selectedPeer}-${peerName}`;

                // Skip if either location is invalid
                if (!selectedLoc || !loc || isNaN(selectedLoc.lat) || isNaN(selectedLoc.lng) || isNaN(loc.lat) || isNaN(loc.lng)) {
                    return;
                }

                expectedConnections.add(key);

                if (this.connections.has(key)) {
                    // Update existing connection
                    this.updateConnection(key, selectedLoc, loc);
                } else {
                    // Create new connection
                    this.createConnection(key, selectedLoc, loc);
                }
            });
        }

        // Remove connections that no longer exist
        this.connections.forEach((_, key) => {
            if (!expectedConnections.has(key)) {
                this.removeConnection(key);
            }
        });
    }

    // Calculate bezier curve points between two locations
    // Trims a fixed distance from each end to avoid crowding the node markers
    calculateCurvePoints(lat1, lng1, lat2, lng2, numPoints = 20) {
        const points = [];

        // Calculate perpendicular offset for curve
        // Use distance-based offset (larger distances = more curve)
        const dx = lng2 - lng1;
        const dy = lat2 - lat1;
        const distance = Math.sqrt(dx * dx + dy * dy);

        // Handle same location - return empty (no line needed)
        if (distance < 0.0001) {
            return [];
        }

        // Calculate midpoint
        const midLat = (lat1 + lat2) / 2;
        const midLng = (lng1 + lng2) / 2;

        // Offset perpendicular to the line (scale with distance)
        const curveAmount = distance * 0.15;
        const perpX = -dy / distance * curveAmount;
        const perpY = dx / distance * curveAmount;

        // Control point for quadratic bezier
        const ctrlLat = midLat + perpY;
        const ctrlLng = midLng + perpX;

        // Fixed trim distance in degrees (~0.5 degrees ≈ 55km at equator)
        // This keeps a consistent gap around markers regardless of line length
        const trimDistance = 0.5;
        const trimT = Math.min(trimDistance / distance, 0.4); // Cap at 40% for very short lines

        const startT = trimT;
        const endT = 1 - trimT;

        // If line is too short after trimming, don't draw it
        if (startT >= endT) {
            return [];
        }

        // Generate points along quadratic bezier curve
        for (let i = 0; i <= numPoints; i++) {
            const t = startT + (i / numPoints) * (endT - startT);
            const t1 = 1 - t;

            // Quadratic bezier formula: B(t) = (1-t)²P0 + 2(1-t)tP1 + t²P2
            const lat = t1 * t1 * lat1 + 2 * t1 * t * ctrlLat + t * t * lat2;
            const lng = t1 * t1 * lng1 + 2 * t1 * t * ctrlLng + t * t * lng2;

            points.push([lat, lng]);
        }

        return points;
    }

    // Create a connection line between two peers
    createConnection(key, loc1, loc2) {
        const curvePoints = this.calculateCurvePoints(loc1.lat, loc1.lng, loc2.lat, loc2.lng);

        // Skip if no points (same location)
        if (curvePoints.length === 0) return;

        const polyline = L.polyline(curvePoints, {
            color: '#58a6ff',
            weight: 2,
            opacity: 0.7,
            smoothFactor: 1
        }).addTo(this.map);

        // Bring markers to front (above connection lines)
        this.markers.forEach(entry => {
            if (entry.marker) entry.marker.bringToFront();
        });

        this.connections.set(key, { polyline });
    }

    // Update an existing connection line
    updateConnection(key, loc1, loc2) {
        const entry = this.connections.get(key);
        if (!entry) return;

        const curvePoints = this.calculateCurvePoints(loc1.lat, loc1.lng, loc2.lat, loc2.lng);
        if (curvePoints.length > 0) {
            entry.polyline.setLatLngs(curvePoints);
        }
    }

    // Remove a connection line
    removeConnection(key) {
        const entry = this.connections.get(key);
        if (!entry) return;

        if (entry.polyline) this.map.removeLayer(entry.polyline);

        this.connections.delete(key);
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

    // Center map on a specific peer
    centerOnPeer(peerName) {
        if (!this.map) return;

        const entry = this.markers.get(peerName);
        if (entry && entry.marker) {
            const latLng = entry.marker.getLatLng();
            this.map.panTo(latLng, { animate: false });
        }
    }

    // Set the selected peer and update marker colors
    setSelectedPeer(peerName) {
        const previousSelected = this.selectedPeer;
        this.selectedPeer = peerName;

        // Update previous selected marker back to normal color
        if (previousSelected && this.markers.has(previousSelected)) {
            const entry = this.markers.get(previousSelected);
            const color = '#3fb950'; // green for online (assume online if marker exists)
            if (entry.marker) {
                entry.marker.setStyle({ fillColor: color, color: color });
            }
            if (entry.circle) {
                entry.circle.setStyle({ fillColor: color, color: color });
            }
        }

        // Update newly selected marker to blue
        if (peerName && this.markers.has(peerName)) {
            const entry = this.markers.get(peerName);
            const color = '#58a6ff'; // blue for selected
            if (entry.marker) {
                entry.marker.setStyle({ fillColor: color, color: color });
            }
            if (entry.circle) {
                entry.circle.setStyle({ fillColor: color, color: color });
            }
        }

        // Update connections to show only from selected peer
        this.updateConnections();

        // Zoom to fit selected peer and all its connections
        this.fitToConnections();
    }

    // Zoom map to fit the selected peer and all connected peers
    fitToConnections() {
        if (!this.map || !this.selectedPeer) return;

        const boundsArray = [];

        // Add selected peer location
        const selectedLoc = this.onlinePeersWithLocation.get(this.selectedPeer);
        if (selectedLoc) {
            boundsArray.push([selectedLoc.lat, selectedLoc.lng]);
        }

        // Add all connected peer locations
        this.onlinePeersWithLocation.forEach((loc, peerName) => {
            if (peerName !== this.selectedPeer) {
                boundsArray.push([loc.lat, loc.lng]);
            }
        });

        // Fit bounds if we have locations
        if (boundsArray.length > 1) {
            const bounds = L.latLngBounds(boundsArray);
            this.map.fitBounds(bounds, { padding: [50, 50], maxZoom: 10, animate: false });
        } else if (boundsArray.length === 1) {
            // Only selected peer - just center on it
            this.map.setView(boundsArray[0], this.map.getZoom(), { animate: false });
        }
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
        this.connections.clear();
        this.onlinePeersWithLocation.clear();
        this.initialized = false;
    }
}
