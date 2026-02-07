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
        this.markers = new Map(); // peerName -> { marker, loc, peer }
        this.connections = new Map(); // "peerA-peerB" -> polyline
        this.accuracyCircle = null; // Single accuracy circle for selected node
        this.bounds = null;
        this.initialized = false;
        this.selectedPeer = null;
        this.selectedPeerData = null; // Full peer data for selected node
        this.onlinePeersWithLocation = new Map(); // peerName -> {lat, lng, exitNode}
        this.pendingFitToConnections = false; // Flag to fit after first updatePeers
        this.skipNextZoom = false; // Flag to skip zoom on next selection (for map clicks)
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
        // Disable scroll wheel zoom to prevent accidental zooming while scrolling page
        this.map = L.map(this.containerId, {
            center: [20, 0],
            zoom: 2,
            scrollWheelZoom: false,
            attributionControl: false,
        });

        // Use CartoDB Dark Matter tiles for dark theme
        L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
            attribution:
                '&copy; <a href="https://www.openstreetmap.org/copyright">OSM</a> &copy; <a href="https://carto.com/attributions">CARTO</a>',
            subdomains: 'abcd',
            maxZoom: 19,
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

        // Group peers by location to detect co-located nodes
        // Only include peers with confirmed location (has source = "manual" or "ip")
        const locationGroups = new Map(); // "lat,lng" -> [peer, ...]
        peers.forEach((peer) => {
            if (!peer.location || !peer.location.source) {
                return; // No location or location not yet determined
            }
            const key = `${peer.location.latitude},${peer.location.longitude}`;
            if (!locationGroups.has(key)) {
                locationGroups.set(key, []);
            }
            locationGroups.get(key).push(peer);
        });

        // Calculate offsets for co-located nodes (arrange in a circle)
        const peerOffsets = new Map(); // peerName -> {latOffset, lngOffset}
        locationGroups.forEach((groupPeers) => {
            if (groupPeers.length > 1) {
                // Offset in degrees (~0.45 degrees ≈ 50km)
                const offsetRadius = 0.45;
                groupPeers.forEach((peer, i) => {
                    const angle = (2 * Math.PI * i) / groupPeers.length;
                    peerOffsets.set(peer.name, {
                        latOffset: offsetRadius * Math.sin(angle),
                        lngOffset: offsetRadius * Math.cos(angle),
                    });
                });
            }
        });

        peers.forEach((peer) => {
            if (!peer.location || !peer.location.source) {
                // No location or location not yet determined - remove marker if exists
                if (this.markers.has(peer.name)) {
                    this.removeMarker(peer.name);
                }
                return;
            }

            hasLocations = true;
            seenPeers.add(peer.name);

            const loc = peer.location;
            let lat = loc.latitude;
            let lng = loc.longitude;

            // Apply offset for co-located nodes
            const offset = peerOffsets.get(peer.name);
            if (offset) {
                lat += offset.latOffset;
                lng += offset.lngOffset;
            }

            boundsArray.push([lat, lng]);

            // Track online peers with location for connection drawing
            if (peer.online) {
                this.onlinePeersWithLocation.set(peer.name, {
                    lat,
                    lng,
                    exitNode: peer.exit_node || '',
                });
            }

            // Update selectedPeerData if this is the selected peer
            if (peer.name === this.selectedPeer) {
                this.selectedPeerData = peer;
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
        // Note: iterate over a copy of keys since removeMarker modifies the Map
        [...this.markers.keys()].forEach((peerName) => {
            if (!seenPeers.has(peerName)) {
                this.removeMarker(peerName);
            }
        });

        // Update connections between online peers
        this.updateConnections();

        // If we had a pending fitToConnections (selection happened before data loaded), do it now
        if (this.pendingFitToConnections && this.onlinePeersWithLocation.size > 0) {
            this.pendingFitToConnections = false;
            this.fitToConnections();
        }

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

        // Fit map to show all markers (only on first load to avoid elastic zoom effect)
        if (boundsArray.length > 0 && this.map && !this.bounds) {
            const bounds = L.latLngBounds(boundsArray);
            // Delay fitBounds if map was just shown to ensure invalidateSize completed
            const fitBoundsDelay = mapSection && mapSection.style.display !== 'none' ? 150 : 0;
            setTimeout(() => {
                if (this.map) {
                    // Disable animation to prevent elastic zoom on page load
                    this.map.fitBounds(bounds, { padding: [50, 50], maxZoom: 10, animate: false });
                }
            }, fitBoundsDelay);
            this.bounds = bounds;
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
            const selectedExitNode = this.selectedPeerData?.exit_node || '';

            // Create connections from selected peer to all other online peers
            this.onlinePeersWithLocation.forEach((loc, peerName) => {
                if (peerName === this.selectedPeer) return;

                const key = `${this.selectedPeer}-${peerName}`;

                // Skip if either location is invalid
                if (
                    !selectedLoc ||
                    !loc ||
                    Number.isNaN(selectedLoc.lat) ||
                    Number.isNaN(selectedLoc.lng) ||
                    Number.isNaN(loc.lat) ||
                    Number.isNaN(loc.lng)
                ) {
                    return;
                }

                // Check if this is an exit path connection
                const isExitPath = selectedExitNode === peerName || loc.exitNode === this.selectedPeer;

                expectedConnections.add(key);

                if (this.connections.has(key)) {
                    // Update existing connection
                    this.updateConnection(key, selectedLoc, loc, isExitPath);
                } else {
                    // Create new connection
                    this.createConnection(key, selectedLoc, loc, isExitPath);
                }
            });
        }

        // Remove connections that no longer exist
        // Note: iterate over a copy of keys since removeConnection modifies the Map
        [...this.connections.keys()].forEach((key) => {
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
        const perpX = (-dy / distance) * curveAmount;
        const perpY = (dx / distance) * curveAmount;

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
    createConnection(key, loc1, loc2, isExitPath = false) {
        const curvePoints = this.calculateCurvePoints(loc1.lat, loc1.lng, loc2.lat, loc2.lng);

        // Skip if no points (same location)
        if (curvePoints.length === 0) return;

        // Use golden color and solid line for exit paths
        const polyline = L.polyline(curvePoints, {
            color: isExitPath ? '#f0a500' : '#58a6ff',
            weight: isExitPath ? 3 : 2,
            opacity: 1,
            smoothFactor: 1,
            dashArray: isExitPath ? null : '6, 4', // Solid for exit, dashed for mesh
        }).addTo(this.map);

        // Bring markers to front (above connection lines)
        this.markers.forEach((entry) => {
            if (entry.marker) entry.marker.bringToFront();
        });

        this.connections.set(key, { polyline, isExitPath });
    }

    // Update an existing connection line
    updateConnection(key, loc1, loc2, isExitPath = false) {
        const entry = this.connections.get(key);
        if (!entry) return;

        const curvePoints = this.calculateCurvePoints(loc1.lat, loc1.lng, loc2.lat, loc2.lng);
        if (curvePoints.length > 0) {
            entry.polyline.setLatLngs(curvePoints);

            // Update style if exit path status changed
            if (entry.isExitPath !== isExitPath) {
                entry.polyline.setStyle({
                    color: isExitPath ? '#f0a500' : '#58a6ff',
                    weight: isExitPath ? 3 : 2,
                    dashArray: isExitPath ? null : '6, 4',
                });
                entry.isExitPath = isExitPath;
            }
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
            fillOpacity: 0.8,
        }).addTo(this.map);

        // Click to select this node (without zooming since user already sees it)
        marker.on('click', () => {
            if (typeof selectNode === 'function') {
                this.skipNextZoom = true;
                selectNode(name);
            }
        });

        // Store location info (accuracy circle is managed at map level, not per-marker)
        this.markers.set(name, { marker, loc, peer });
    }

    updateMarker(name, lat, lng, color, peer, loc) {
        const entry = this.markers.get(name);
        if (!entry) return;

        const { marker } = entry;

        // Update position
        marker.setLatLng([lat, lng]);

        // Update style
        marker.setStyle({
            fillColor: color,
            color: color,
        });

        // Store updated location info
        entry.loc = loc;
        entry.peer = peer;

        // Update accuracy circle if this is the selected node
        if (name === this.selectedPeer && this.accuracyCircle) {
            this.accuracyCircle.setLatLng([loc.latitude, loc.longitude]);
            if (loc.accuracy) {
                this.accuracyCircle.setRadius(loc.accuracy);
            }
        }
    }

    removeMarker(name) {
        const entry = this.markers.get(name);
        if (!entry) return;

        if (entry.marker) this.map.removeLayer(entry.marker);

        // If removing the selected peer, also remove the accuracy circle
        if (name === this.selectedPeer && this.accuracyCircle) {
            this.map.removeLayer(this.accuracyCircle);
            this.accuracyCircle = null;
        }

        this.markers.delete(name);
    }

    // Set the selected peer and update marker colors
    setSelectedPeer(peerName) {
        const previousSelected = this.selectedPeer;
        this.selectedPeer = peerName;

        // Update previous selected marker back to normal color
        if (previousSelected && this.markers.has(previousSelected)) {
            const entry = this.markers.get(previousSelected);
            // Use correct color based on online status
            const isOnline = entry.peer?.online;
            const color = isOnline ? '#3fb950' : '#6b7280'; // green for online, grey for offline
            if (entry.marker) {
                entry.marker.setStyle({ fillColor: color, color: color });
            }
        }

        // Update newly selected marker to blue
        if (peerName && this.markers.has(peerName)) {
            const entry = this.markers.get(peerName);
            const color = '#58a6ff'; // blue for selected
            if (entry.marker) {
                entry.marker.setStyle({ fillColor: color, color: color });
            }

            // Manage single accuracy circle (only for IP geolocation, never for manual)
            const shouldShowCircle =
                entry.loc &&
                entry.loc.source === 'ip' &&
                entry.loc.accuracy &&
                entry.loc.accuracy > 1000 &&
                entry.peer &&
                entry.peer.online;

            if (shouldShowCircle) {
                // Move existing circle or create new one
                if (this.accuracyCircle) {
                    this.accuracyCircle.setLatLng([entry.loc.latitude, entry.loc.longitude]);
                    this.accuracyCircle.setRadius(entry.loc.accuracy);
                } else {
                    this.accuracyCircle = L.circle([entry.loc.latitude, entry.loc.longitude], {
                        radius: entry.loc.accuracy,
                        fillColor: color,
                        color: color,
                        weight: 1,
                        opacity: 0.3,
                        fillOpacity: 0.1,
                        dashArray: '5, 5',
                    }).addTo(this.map);
                }
            } else {
                // Remove circle if source is manual or conditions not met
                if (this.accuracyCircle) {
                    this.map.removeLayer(this.accuracyCircle);
                    this.accuracyCircle = null;
                }
            }

            // Bring marker to front
            if (entry.marker) entry.marker.bringToFront();
        } else {
            // No peer selected or peer not found - remove accuracy circle
            if (this.accuracyCircle) {
                this.map.removeLayer(this.accuracyCircle);
                this.accuracyCircle = null;
            }
        }

        // Update connections to show only from selected peer
        this.updateConnections();

        // Zoom to fit selected peer and all its connections (unless skip flag is set)
        // Skip flag is set when user clicks on map marker (they already see it)
        if (this.skipNextZoom) {
            this.skipNextZoom = false;
        } else {
            // If map has no data yet, defer until updatePeers populates it
            if (this.onlinePeersWithLocation.size === 0) {
                this.pendingFitToConnections = true;
            } else {
                this.fitToConnections();
            }
        }
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
        if (this.accuracyCircle) {
            this.accuracyCircle = null;
        }
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

// Expose to window for use in app.js
window.NodeMap = NodeMap;
