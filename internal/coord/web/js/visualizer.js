// Node Visualizer for TunnelMesh Dashboard
// Implements a pan/zoomable canvas showing mesh topology

// =============================================================================
// Constants and Types
// =============================================================================

const NodeType = {
    STANDARD: 'standard',
    WG_CONCENTRATOR: 'wg_concentrator',
    EXIT_NODE: 'exit_node'
};

const CARD_WIDTH = 200;
const CARD_HEIGHT = 80;
const TAB_HEIGHT = 20;
const COLUMN_SPACING = 280;
const ROW_SPACING = 100;
const STACK_OFFSET_X = 10;
const STACK_OFFSET_Y = 4;
const CONNECTION_DOT_RADIUS = 5;

// Colors matching dashboard theme
const COLORS = {
    background: '#0d1117',
    cardFill: '#21262d',
    cardStroke: '#30363d',
    text: '#e6edf3',
    textDim: '#8b949e',
    online: '#3fb950',
    nat: '#d29922',
    offline: '#f85149',
    selected: '#238636',
    selectedStroke: '#3fb950',
    hovered: '#1f6feb',
    hoveredStroke: '#58a6ff',
    connection: '#30363d',
    connectionHighlight: '#58a6ff'
};

// =============================================================================
// VisualizerNode Class
// =============================================================================

class VisualizerNode {
    constructor(peer, domainSuffix, nodeType = NodeType.STANDARD) {
        // Identity from peer data
        this.id = peer.name;
        this.name = peer.name;
        this.meshIP = peer.mesh_ip;
        this.online = peer.online;
        this.connectable = peer.connectable;
        this.behindNAT = peer.behind_nat;
        this.activeTunnels = peer.stats?.active_tunnels ?? 0;
        this.version = peer.version || '';
        this.nodeType = nodeType;

        // Build DNS name with truncation
        const fullDns = peer.name + (domainSuffix || '');
        this.dnsName = fullDns.length > 25 ? fullDns.substring(0, 22) + '...' : fullDns;

        // Layout positions
        this.x = 0;
        this.y = 0;
        this.targetX = 0;
        this.targetY = 0;

        // Visual state
        this.selected = false;
        this.hovered = false;
        this.stackIndex = 0;
        this.stackSize = 1;
    }

    get category() {
        return `${this.nodeType}_${this.connectable}_${this.online}`;
    }

    // Get stroke color based on state
    getStrokeColor() {
        if (this.selected) return COLORS.selectedStroke;
        if (this.hovered) return COLORS.hoveredStroke;
        if (!this.online) return COLORS.offline;
        if (!this.connectable) return COLORS.nat;
        return COLORS.online;
    }

    // Get fill color based on state
    getFillColor() {
        if (this.selected) return COLORS.selected;
        if (this.hovered) return COLORS.hovered;
        return COLORS.cardFill;
    }

    // Get status icon color
    getIconColor() {
        if (this.selected) return '#ffffff';
        if (this.hovered) return COLORS.hoveredStroke;
        if (!this.online) return COLORS.offline;
        if (!this.connectable) return COLORS.nat;
        return COLORS.online;
    }
}

// =============================================================================
// Core Logic Functions
// =============================================================================

// Check if source node can reach target node
function canNodeReach(source, target) {
    // Offline nodes can't communicate
    if (!source.online || !target.online) {
        return false;
    }
    // All online nodes can reach each other (direct if connectable, else via relay)
    return true;
}

// Convert screen coordinates to world coordinates
function screenToWorld(screenX, screenY, transform) {
    return {
        x: (screenX - transform.offsetX) / transform.scale,
        y: (screenY - transform.offsetY) / transform.scale
    };
}

// Convert world coordinates to screen coordinates
function worldToScreen(worldX, worldY, transform) {
    return {
        x: worldX * transform.scale + transform.offsetX,
        y: worldY * transform.scale + transform.offsetY
    };
}

// =============================================================================
// Layout Algorithm
// =============================================================================

function calculateLayout(nodes, selectedId, canvasWidth, canvasHeight) {
    const centerX = canvasWidth / 2;
    const centerY = canvasHeight / 2;

    if (!selectedId || !nodes.has(selectedId)) {
        // No selection - grid layout
        layoutGrid(nodes, centerX, centerY);
        return;
    }

    const selectedNode = nodes.get(selectedId);

    // Position selected node at center
    selectedNode.targetX = centerX;
    selectedNode.targetY = centerY;

    // Classify other nodes
    const incoming = [];  // Can reach selected
    const outgoing = [];  // Selected can reach

    for (const [id, node] of nodes) {
        if (id === selectedId) continue;

        const canReachSelected = canNodeReach(node, selectedNode);
        const canBeReached = canNodeReach(selectedNode, node);

        // In a mesh, most nodes are both incoming and outgoing
        // Prioritize by connectable status for visual clarity
        if (node.connectable && !selectedNode.connectable) {
            // Node is connectable, selected is NAT - show as outgoing (selected reaches them)
            outgoing.push(node);
        } else if (!node.connectable && selectedNode.connectable) {
            // Node is NAT, selected is connectable - show as incoming (they reach selected)
            incoming.push(node);
        } else if (canReachSelected) {
            // Default: show online nodes that can reach selected as incoming
            incoming.push(node);
        }
    }

    // Layout columns
    layoutColumn(incoming, centerX - COLUMN_SPACING, centerY, { stackOffset: STACK_OFFSET_X, rowSpacing: ROW_SPACING });
    layoutColumn(outgoing, centerX + COLUMN_SPACING, centerY, { stackOffset: STACK_OFFSET_X, rowSpacing: ROW_SPACING });
}

function layoutGrid(nodes, centerX, centerY) {
    const nodeArray = Array.from(nodes.values());
    const count = nodeArray.length;
    if (count === 0) return;

    const cols = Math.ceil(Math.sqrt(count));
    const rows = Math.ceil(count / cols);
    const spacing = CARD_WIDTH + 40;

    nodeArray.forEach((node, i) => {
        const col = i % cols;
        const row = Math.floor(i / cols);
        node.targetX = centerX + (col - (cols - 1) / 2) * spacing;
        node.targetY = centerY + (row - (rows - 1) / 2) * (CARD_HEIGHT + ROW_SPACING);
    });
}

function layoutColumn(nodes, centerX, centerY, config) {
    if (nodes.length === 0) return;

    // Group by category
    const groups = new Map();
    for (const node of nodes) {
        const cat = node.category;
        if (!groups.has(cat)) groups.set(cat, []);
        groups.get(cat).push(node);
    }

    // Sort groups for consistent ordering
    const groupArray = Array.from(groups.entries()).sort((a, b) => a[0].localeCompare(b[0]));

    // Calculate total height
    const totalHeight = groupArray.length * config.rowSpacing;
    let currentY = centerY - totalHeight / 2;

    for (const [category, groupNodes] of groupArray) {
        const stackSize = groupNodes.length;

        groupNodes.forEach((node, idx) => {
            node.stackIndex = idx;
            node.stackSize = stackSize;

            // Stack horizontally with small offset
            node.targetX = centerX + (idx - (stackSize - 1) / 2) * config.stackOffset;
            node.targetY = currentY + idx * STACK_OFFSET_Y;
        });

        currentY += config.rowSpacing;
    }
}

// =============================================================================
// Hit Testing
// =============================================================================

function hitTest(screenX, screenY, nodes, transform) {
    const world = screenToWorld(screenX, screenY, transform);

    // Check nodes in reverse order (topmost first)
    const nodeArray = Array.from(nodes.values()).reverse();

    for (const node of nodeArray) {
        const halfWidth = CARD_WIDTH / 2;
        const halfHeight = CARD_HEIGHT / 2;

        if (world.x >= node.x - halfWidth && world.x <= node.x + halfWidth &&
            world.y >= node.y - halfHeight - TAB_HEIGHT && world.y <= node.y + halfHeight) {
            return node;
        }
    }

    return null;
}

// =============================================================================
// NodeVisualizer Class
// =============================================================================

class NodeVisualizer {
    constructor(canvas) {
        this.canvas = canvas;
        this.ctx = canvas.getContext('2d');

        // Transform state (pan/zoom)
        this.transform = {
            offsetX: 0,
            offsetY: 0,
            scale: 1
        };

        // Data
        this.nodes = new Map();
        this.selectedNodeId = null;
        this.hoveredNodeId = null;
        this.domainSuffix = '.tunnelmesh';

        // Animation
        this.animating = false;
        this.animationProgress = 1;

        // Interaction state
        this.isDragging = false;
        this.lastMouseX = 0;
        this.lastMouseY = 0;

        // Callbacks
        this.onNodeSelected = null;

        // Setup
        this.setupInteraction();
        this.resize();

        // Initial render
        this.render();
    }

    // -------------------------------------------------------------------------
    // Public API
    // -------------------------------------------------------------------------

    setDomainSuffix(suffix) {
        this.domainSuffix = suffix;
    }

    syncNodes(peers, wgConcentratorName = null) {
        const existingIds = new Set(this.nodes.keys());
        const newIds = new Set(peers.map(p => p.name));

        // Remove nodes that no longer exist
        for (const id of existingIds) {
            if (!newIds.has(id)) {
                this.nodes.delete(id);
            }
        }

        // Update or add nodes
        for (const peer of peers) {
            const nodeType = peer.name === wgConcentratorName ? NodeType.WG_CONCENTRATOR : NodeType.STANDARD;

            if (this.nodes.has(peer.name)) {
                // Update existing node
                const node = this.nodes.get(peer.name);
                node.online = peer.online;
                node.connectable = peer.connectable;
                node.behindNAT = peer.behind_nat;
                node.activeTunnels = peer.stats?.active_tunnels ?? 0;
                node.version = peer.version || '';
                node.nodeType = nodeType;
            } else {
                // Add new node
                const node = new VisualizerNode(peer, this.domainSuffix, nodeType);
                this.nodes.set(peer.name, node);
            }
        }

        // Recalculate layout
        this.recalculateLayout();
    }

    selectNode(nodeId) {
        // Deselect previous
        if (this.selectedNodeId && this.nodes.has(this.selectedNodeId)) {
            this.nodes.get(this.selectedNodeId).selected = false;
        }

        this.selectedNodeId = nodeId;

        // Select new
        if (nodeId && this.nodes.has(nodeId)) {
            this.nodes.get(nodeId).selected = true;
        }

        this.recalculateLayout();
        this.startAnimation();

        // Notify callback
        if (this.onNodeSelected) {
            this.onNodeSelected(nodeId);
        }
    }

    resize() {
        const rect = this.canvas.parentElement.getBoundingClientRect();
        const dpr = window.devicePixelRatio || 1;

        this.canvas.width = rect.width * dpr;
        this.canvas.height = rect.height * dpr;
        this.canvas.style.width = rect.width + 'px';
        this.canvas.style.height = rect.height + 'px';

        this.ctx.scale(dpr, dpr);

        this.recalculateLayout();
        this.render();
    }

    // -------------------------------------------------------------------------
    // Layout
    // -------------------------------------------------------------------------

    recalculateLayout() {
        const rect = this.canvas.getBoundingClientRect();
        calculateLayout(this.nodes, this.selectedNodeId, rect.width, rect.height);
    }

    // -------------------------------------------------------------------------
    // Animation
    // -------------------------------------------------------------------------

    startAnimation() {
        this.animationProgress = 0;
        if (!this.animating) {
            this.animating = true;
            this.animate();
        }
    }

    animate() {
        this.animationProgress += 0.08;

        if (this.animationProgress >= 1) {
            this.animationProgress = 1;
            this.animating = false;
        }

        // Ease out cubic
        const t = 1 - Math.pow(1 - this.animationProgress, 3);

        // Interpolate positions
        for (const node of this.nodes.values()) {
            node.x = node.x + (node.targetX - node.x) * t;
            node.y = node.y + (node.targetY - node.y) * t;
        }

        this.render();

        if (this.animating) {
            requestAnimationFrame(() => this.animate());
        }
    }

    // -------------------------------------------------------------------------
    // Interaction
    // -------------------------------------------------------------------------

    setupInteraction() {
        // Mouse events
        this.canvas.addEventListener('mousedown', (e) => this.onMouseDown(e));
        this.canvas.addEventListener('mousemove', (e) => this.onMouseMove(e));
        this.canvas.addEventListener('mouseup', () => this.onMouseUp());
        this.canvas.addEventListener('mouseleave', () => this.onMouseLeave());
        this.canvas.addEventListener('wheel', (e) => this.onWheel(e), { passive: false });
        this.canvas.addEventListener('click', (e) => this.onClick(e));

        // Touch events
        this.canvas.addEventListener('touchstart', (e) => this.onTouchStart(e), { passive: false });
        this.canvas.addEventListener('touchmove', (e) => this.onTouchMove(e), { passive: false });
        this.canvas.addEventListener('touchend', () => this.onTouchEnd());

        // Resize
        window.addEventListener('resize', () => this.resize());
    }

    onMouseDown(e) {
        this.isDragging = true;
        this.lastMouseX = e.clientX;
        this.lastMouseY = e.clientY;
        this.canvas.style.cursor = 'grabbing';
    }

    onMouseMove(e) {
        const rect = this.canvas.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const y = e.clientY - rect.top;

        if (this.isDragging) {
            const dx = e.clientX - this.lastMouseX;
            const dy = e.clientY - this.lastMouseY;
            this.transform.offsetX += dx;
            this.transform.offsetY += dy;
            this.lastMouseX = e.clientX;
            this.lastMouseY = e.clientY;
            this.render();
        } else {
            // Update hover
            const node = hitTest(x, y, this.nodes, this.transform);
            const newHoveredId = node ? node.id : null;

            if (newHoveredId !== this.hoveredNodeId) {
                if (this.hoveredNodeId && this.nodes.has(this.hoveredNodeId)) {
                    this.nodes.get(this.hoveredNodeId).hovered = false;
                }
                this.hoveredNodeId = newHoveredId;
                if (newHoveredId && this.nodes.has(newHoveredId)) {
                    this.nodes.get(newHoveredId).hovered = true;
                }
                this.canvas.style.cursor = newHoveredId ? 'pointer' : 'grab';
                this.render();
            }
        }
    }

    onMouseUp() {
        this.isDragging = false;
        this.canvas.style.cursor = this.hoveredNodeId ? 'pointer' : 'grab';
    }

    onMouseLeave() {
        this.isDragging = false;
        if (this.hoveredNodeId && this.nodes.has(this.hoveredNodeId)) {
            this.nodes.get(this.hoveredNodeId).hovered = false;
        }
        this.hoveredNodeId = null;
        this.canvas.style.cursor = 'grab';
        this.render();
    }

    onClick(e) {
        if (this.isDragging) return;

        const rect = this.canvas.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const y = e.clientY - rect.top;

        const node = hitTest(x, y, this.nodes, this.transform);
        if (node) {
            this.selectNode(node.id === this.selectedNodeId ? null : node.id);
        } else {
            this.selectNode(null);
        }
    }

    onWheel(e) {
        e.preventDefault();

        const rect = this.canvas.getBoundingClientRect();
        const mouseX = e.clientX - rect.left;
        const mouseY = e.clientY - rect.top;

        const zoomFactor = e.deltaY > 0 ? 0.9 : 1.1;
        const minScale = 0.3;
        const maxScale = 3;

        const newScale = Math.max(minScale, Math.min(maxScale, this.transform.scale * zoomFactor));

        // Zoom toward mouse position
        const scaleChange = newScale / this.transform.scale;
        this.transform.offsetX = mouseX - (mouseX - this.transform.offsetX) * scaleChange;
        this.transform.offsetY = mouseY - (mouseY - this.transform.offsetY) * scaleChange;
        this.transform.scale = newScale;

        this.render();
    }

    // Touch handling
    onTouchStart(e) {
        if (e.touches.length === 1) {
            e.preventDefault();
            this.isDragging = true;
            this.lastMouseX = e.touches[0].clientX;
            this.lastMouseY = e.touches[0].clientY;
        }
    }

    onTouchMove(e) {
        if (e.touches.length === 1 && this.isDragging) {
            e.preventDefault();
            const dx = e.touches[0].clientX - this.lastMouseX;
            const dy = e.touches[0].clientY - this.lastMouseY;
            this.transform.offsetX += dx;
            this.transform.offsetY += dy;
            this.lastMouseX = e.touches[0].clientX;
            this.lastMouseY = e.touches[0].clientY;
            this.render();
        }
    }

    onTouchEnd() {
        this.isDragging = false;
    }

    // -------------------------------------------------------------------------
    // Rendering
    // -------------------------------------------------------------------------

    render() {
        const ctx = this.ctx;
        const rect = this.canvas.getBoundingClientRect();
        const width = rect.width;
        const height = rect.height;

        // Clear
        ctx.fillStyle = COLORS.background;
        ctx.fillRect(0, 0, width, height);

        if (this.nodes.size === 0) {
            // Draw empty state
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '14px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'center';
            ctx.fillText('No peers connected', width / 2, height / 2);
            return;
        }

        // Apply transform
        ctx.save();
        ctx.translate(this.transform.offsetX, this.transform.offsetY);
        ctx.scale(this.transform.scale, this.transform.scale);

        // Draw connections
        this.renderConnections(ctx);

        // Draw nodes (sorted so selected is on top)
        const sortedNodes = Array.from(this.nodes.values()).sort((a, b) => {
            if (a.selected) return 1;
            if (b.selected) return -1;
            if (a.hovered) return 1;
            if (b.hovered) return -1;
            return 0;
        });

        for (const node of sortedNodes) {
            this.renderNode(ctx, node);
        }

        ctx.restore();
    }

    renderConnections(ctx) {
        if (!this.selectedNodeId) return;

        const selected = this.nodes.get(this.selectedNodeId);
        if (!selected) return;

        for (const node of this.nodes.values()) {
            if (node.id === this.selectedNodeId) continue;

            const canReach = canNodeReach(node, selected) || canNodeReach(selected, node);
            if (!canReach) continue;

            // Draw curved connection
            ctx.beginPath();
            ctx.strokeStyle = COLORS.connectionHighlight;
            ctx.lineWidth = 2;

            const startX = node.x + CARD_WIDTH / 2;
            const startY = node.y;
            const endX = selected.x - CARD_WIDTH / 2;
            const endY = selected.y;

            // Control point for curve
            const midX = (startX + endX) / 2;

            ctx.moveTo(startX, startY);
            ctx.bezierCurveTo(midX, startY, midX, endY, endX, endY);
            ctx.stroke();

            // Connection dots
            ctx.fillStyle = COLORS.connectionHighlight;
            ctx.beginPath();
            ctx.arc(startX, startY, CONNECTION_DOT_RADIUS, 0, Math.PI * 2);
            ctx.fill();
            ctx.beginPath();
            ctx.arc(endX, endY, CONNECTION_DOT_RADIUS, 0, Math.PI * 2);
            ctx.fill();
        }
    }

    renderNode(ctx, node) {
        const x = node.x - CARD_WIDTH / 2;
        const y = node.y - CARD_HEIGHT / 2;

        const strokeColor = node.getStrokeColor();
        const fillColor = node.getFillColor();
        const iconColor = node.getIconColor();

        // Draw tab label
        const tabText = node.name;
        ctx.font = 'bold 12px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        const tabWidth = ctx.measureText(tabText).width + 16;
        const tabX = x;
        const tabY = y - TAB_HEIGHT;

        // Tab background
        ctx.fillStyle = fillColor;
        ctx.beginPath();
        ctx.roundRect(tabX, tabY, tabWidth, TAB_HEIGHT + 4, [4, 4, 0, 0]);
        ctx.fill();
        ctx.strokeStyle = strokeColor;
        ctx.lineWidth = node.selected || node.hovered ? 2 : 1;
        ctx.stroke();

        // Tab text
        ctx.fillStyle = COLORS.text;
        ctx.textAlign = 'left';
        ctx.textBaseline = 'middle';
        ctx.fillText(tabText, tabX + 8, tabY + TAB_HEIGHT / 2);

        // Card background
        ctx.fillStyle = fillColor;
        ctx.beginPath();
        ctx.roundRect(x, y, CARD_WIDTH, CARD_HEIGHT, 6);
        ctx.fill();
        ctx.strokeStyle = strokeColor;
        ctx.lineWidth = node.selected || node.hovered ? 2 : 1;
        ctx.stroke();

        // Content padding
        const contentX = x + 12;
        const contentY = y + 16;
        const lineHeight = 18;

        // Line 1: Status icon + DNS name + version
        // Status icon
        if (node.nodeType === NodeType.WG_CONCENTRATOR) {
            // Shield icon for WG concentrator
            this.drawShieldIcon(ctx, contentX, contentY - 2, iconColor);
        } else {
            // Circle for standard node
            ctx.fillStyle = iconColor;
            ctx.beginPath();
            ctx.arc(contentX + 5, contentY, 5, 0, Math.PI * 2);
            ctx.fill();
        }

        // DNS name
        ctx.fillStyle = COLORS.text;
        ctx.font = '13px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        ctx.textAlign = 'left';
        ctx.fillText(node.dnsName, contentX + 16, contentY + 4);

        // Version (right aligned)
        if (node.version) {
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '11px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'right';
            ctx.fillText(node.version, x + CARD_WIDTH - 12, contentY + 4);
        }

        // Line 2: Mesh IP
        ctx.fillStyle = COLORS.textDim;
        ctx.font = '12px monospace';
        ctx.textAlign = 'left';
        ctx.fillText(node.meshIP, contentX + 16, contentY + lineHeight + 4);

        // Line 3: Tunnel count
        ctx.fillStyle = COLORS.textDim;
        ctx.font = '12px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        const tunnelText = node.activeTunnels === 1 ? '1 tunnel' : `${node.activeTunnels} tunnels`;
        ctx.fillText(tunnelText, contentX + 16, contentY + lineHeight * 2 + 4);
    }

    drawShieldIcon(ctx, x, y, color) {
        ctx.fillStyle = color;
        ctx.beginPath();
        // Simple shield shape
        ctx.moveTo(x + 5, y - 6);
        ctx.lineTo(x + 10, y - 4);
        ctx.lineTo(x + 10, y + 2);
        ctx.quadraticCurveTo(x + 10, y + 6, x + 5, y + 8);
        ctx.quadraticCurveTo(x, y + 6, x, y + 2);
        ctx.lineTo(x, y - 4);
        ctx.closePath();
        ctx.fill();
    }
}

// Export for use in app.js
window.NodeVisualizer = NodeVisualizer;
window.NodeType = NodeType;
