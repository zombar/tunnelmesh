// Node Visualizer for TunnelMesh Dashboard
// Shows mesh topology with selected node centered

// =============================================================================
// Constants and Types
// =============================================================================

const NodeType = {
    STANDARD: 'standard',
    WG_CONCENTRATOR: 'wg_concentrator',
    EXIT_NODE: 'exit_node'
};

const CARD_WIDTH = 180;
const CARD_HEIGHT = 70;
const TAB_HEIGHT = 20;
const ROW_SPACING = 100;  // Vertical spacing between spread nodes
const CONNECTION_DOT_RADIUS = 4;
const MAX_VISIBLE_NODES = 3;  // Show max 3 nodes per column, then "+ N more"

// Colors matching dashboard theme - simplified uniform styling
const COLORS = {
    background: '#0d1117',
    cardFill: '#21262d',
    cardStroke: '#484f58',  // Light grey stroke for all nodes
    text: '#e6edf3',
    textDim: '#8b949e',
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
        this.visible = true;  // Whether to render this node
    }

    get category() {
        return `${this.nodeType}_${this.connectable}_${this.online}`;
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

// =============================================================================
// Layout Algorithm
// =============================================================================

function calculateLayout(nodes, selectedId, canvasWidth, canvasHeight, stackInfo) {
    const centerX = canvasWidth / 2;
    const centerY = canvasHeight / 2;

    // Dynamic column spacing based on canvas width (adjust to fit)
    const columnSpacing = Math.min(280, (canvasWidth - CARD_WIDTH * 3) / 2.5);

    // Reset stack info
    stackInfo.left = { total: 0, hidden: 0 };
    stackInfo.right = { total: 0, hidden: 0 };
    stackInfo.columnSpacing = columnSpacing;

    if (!selectedId || !nodes.has(selectedId)) {
        // No selection - show prompt
        return;
    }

    const selectedNode = nodes.get(selectedId);

    // Position selected node at center
    selectedNode.targetX = centerX;
    selectedNode.targetY = centerY;
    selectedNode.visible = true;

    // Classify other nodes by connectivity type:
    // Left (incoming): NAT nodes that must connect TO the network
    // Right (outgoing): Connectable nodes that can be reached directly
    const incoming = [];  // NAT nodes
    const outgoing = [];  // Connectable nodes

    for (const [id, node] of nodes) {
        if (id === selectedId) continue;
        if (!node.online) continue;  // Skip offline nodes

        if (node.connectable) {
            // Connectable nodes go on right (outgoing targets)
            outgoing.push(node);
        } else {
            // NAT nodes go on left (incoming connections)
            incoming.push(node);
        }
    }

    // Layout columns - spread nodes vertically
    layoutColumn(incoming, centerX - columnSpacing, centerY, stackInfo.left);
    layoutColumn(outgoing, centerX + columnSpacing, centerY, stackInfo.right);
}

function layoutColumn(nodes, centerX, centerY, stackInfo) {
    if (nodes.length === 0) return;

    stackInfo.total = nodes.length;

    // Sort nodes for consistent ordering (by name)
    nodes.sort((a, b) => a.name.localeCompare(b.name));

    // Limit visible nodes
    const visibleCount = Math.min(nodes.length, MAX_VISIBLE_NODES);
    const hiddenCount = nodes.length - visibleCount;
    stackInfo.hidden = hiddenCount;

    // Calculate total height for visible nodes
    const totalHeight = (visibleCount - 1) * ROW_SPACING;
    let currentY = centerY - totalHeight / 2;

    // Position each visible node vertically spread
    for (let i = 0; i < nodes.length; i++) {
        const node = nodes[i];
        node.stackIndex = i;
        node.stackSize = nodes.length;

        if (i < MAX_VISIBLE_NODES) {
            node.visible = true;
            node.targetX = centerX;
            node.targetY = currentY;
            currentY += ROW_SPACING;
        } else {
            node.visible = false;
        }
    }
}

// =============================================================================
// Hit Testing
// =============================================================================

function hitTest(screenX, screenY, nodes) {
    // Check visible nodes in reverse order (topmost first)
    const nodeArray = Array.from(nodes.values()).filter(n => n.visible).reverse();

    for (const node of nodeArray) {
        const halfWidth = CARD_WIDTH / 2;
        const halfHeight = CARD_HEIGHT / 2;

        if (screenX >= node.x - halfWidth && screenX <= node.x + halfWidth &&
            screenY >= node.y - halfHeight - TAB_HEIGHT && screenY <= node.y + halfHeight) {
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

        // Data
        this.nodes = new Map();
        this.selectedNodeId = null;
        this.hoveredNodeId = null;
        this.domainSuffix = '.tunnelmesh';

        // Stack info for "+ N more" labels
        this.stackInfo = {
            left: { total: 0, hidden: 0 },
            right: { total: 0, hidden: 0 }
        };

        // Animation
        this.animating = false;
        this.animationProgress = 1;

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

        // Auto-select first node if none selected
        if (!this.selectedNodeId && this.nodes.size > 0) {
            const firstNode = this.nodes.values().next().value;
            this.selectNode(firstNode.id);
        } else {
            // Recalculate layout
            this.recalculateLayout();
        }
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
        calculateLayout(this.nodes, this.selectedNodeId, rect.width, rect.height, this.stackInfo);
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
        this.animationProgress += 0.12;

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
    // Interaction (simplified - no pan/zoom)
    // -------------------------------------------------------------------------

    setupInteraction() {
        this.canvas.addEventListener('mousemove', (e) => this.onMouseMove(e));
        this.canvas.addEventListener('mouseleave', () => this.onMouseLeave());
        this.canvas.addEventListener('click', (e) => this.onClick(e));

        // Resize
        window.addEventListener('resize', () => this.resize());
    }

    onMouseMove(e) {
        const rect = this.canvas.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const y = e.clientY - rect.top;

        // Update hover
        const node = hitTest(x, y, this.nodes);
        const newHoveredId = node ? node.id : null;

        if (newHoveredId !== this.hoveredNodeId) {
            if (this.hoveredNodeId && this.nodes.has(this.hoveredNodeId)) {
                this.nodes.get(this.hoveredNodeId).hovered = false;
            }
            this.hoveredNodeId = newHoveredId;
            if (newHoveredId && this.nodes.has(newHoveredId)) {
                this.nodes.get(newHoveredId).hovered = true;
            }
            this.canvas.style.cursor = newHoveredId ? 'pointer' : 'default';
            this.render();
        }
    }

    onMouseLeave() {
        if (this.hoveredNodeId && this.nodes.has(this.hoveredNodeId)) {
            this.nodes.get(this.hoveredNodeId).hovered = false;
        }
        this.hoveredNodeId = null;
        this.canvas.style.cursor = 'default';
        this.render();
    }

    onClick(e) {
        const rect = this.canvas.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const y = e.clientY - rect.top;

        const node = hitTest(x, y, this.nodes);
        if (node) {
            this.selectNode(node.id);
        }
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

        if (!this.selectedNodeId) {
            // Draw prompt to select
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '14px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'center';
            ctx.fillText('Select a peer to view connections', width / 2, height / 2);
            return;
        }

        // Draw connections first (behind nodes)
        this.renderConnections(ctx);

        // Draw visible nodes (sorted so selected is on top)
        const visibleNodes = Array.from(this.nodes.values())
            .filter(n => n.visible)
            .sort((a, b) => {
                if (a.selected) return 1;
                if (b.selected) return -1;
                if (a.hovered) return 1;
                if (b.hovered) return -1;
                // Sort by stack index so top cards render last
                return a.stackIndex - b.stackIndex;
            });

        for (const node of visibleNodes) {
            this.renderNode(ctx, node);
        }

        // Draw "+ N more" labels
        this.renderStackLabels(ctx, width, height);
    }

    renderStackLabels(ctx, width, height) {
        const selected = this.nodes.get(this.selectedNodeId);
        if (!selected) return;

        const columnSpacing = this.stackInfo.columnSpacing || 280;

        ctx.font = '12px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        ctx.fillStyle = COLORS.textDim;

        // Left side label
        if (this.stackInfo.left.hidden > 0) {
            ctx.textAlign = 'center';
            const labelX = selected.x - columnSpacing;
            const labelY = height - 20;
            ctx.fillText(`+ ${this.stackInfo.left.hidden} more`, labelX, labelY);
        }

        // Right side label
        if (this.stackInfo.right.hidden > 0) {
            ctx.textAlign = 'center';
            const labelX = selected.x + columnSpacing;
            const labelY = height - 20;
            ctx.fillText(`+ ${this.stackInfo.right.hidden} more`, labelX, labelY);
        }
    }

    renderConnections(ctx) {
        const selected = this.nodes.get(this.selectedNodeId);
        if (!selected) return;

        for (const node of this.nodes.values()) {
            if (node.id === this.selectedNodeId) continue;
            if (!node.visible) continue;

            const canReach = canNodeReach(node, selected) || canNodeReach(selected, node);
            if (!canReach) continue;

            // Determine which side
            const isLeft = node.x < selected.x;

            // Draw curved connection
            ctx.beginPath();
            ctx.strokeStyle = COLORS.connectionHighlight;
            ctx.lineWidth = 2;

            const startX = isLeft ? node.x + CARD_WIDTH / 2 : node.x - CARD_WIDTH / 2;
            const startY = node.y;
            const endX = isLeft ? selected.x - CARD_WIDTH / 2 : selected.x + CARD_WIDTH / 2;
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

        // Draw tab label
        const tabText = node.name;
        ctx.font = 'bold 11px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        const tabWidth = ctx.measureText(tabText).width + 14;
        const tabX = x;
        const tabY = y - TAB_HEIGHT;

        // Draw combined tab + card shape (tab flows into card)
        ctx.fillStyle = COLORS.cardFill;
        ctx.strokeStyle = COLORS.cardStroke;
        ctx.lineWidth = 1;

        // Draw tab (no separate stroke - part of card)
        ctx.beginPath();
        ctx.moveTo(tabX, y);  // Start at top-left of card
        ctx.lineTo(tabX, tabY + 4);  // Go up to tab
        ctx.arcTo(tabX, tabY, tabX + 4, tabY, 4);  // Top-left corner of tab
        ctx.lineTo(tabX + tabWidth - 4, tabY);  // Top edge of tab
        ctx.arcTo(tabX + tabWidth, tabY, tabX + tabWidth, tabY + 4, 4);  // Top-right corner of tab
        ctx.lineTo(tabX + tabWidth, y);  // Down to card top
        ctx.lineTo(x + CARD_WIDTH - 6, y);  // Continue along card top
        ctx.arcTo(x + CARD_WIDTH, y, x + CARD_WIDTH, y + 6, 6);  // Top-right of card
        ctx.lineTo(x + CARD_WIDTH, y + CARD_HEIGHT - 6);  // Right edge
        ctx.arcTo(x + CARD_WIDTH, y + CARD_HEIGHT, x + CARD_WIDTH - 6, y + CARD_HEIGHT, 6);  // Bottom-right
        ctx.lineTo(x + 6, y + CARD_HEIGHT);  // Bottom edge
        ctx.arcTo(x, y + CARD_HEIGHT, x, y + CARD_HEIGHT - 6, 6);  // Bottom-left
        ctx.lineTo(x, y);  // Left edge back to start
        ctx.closePath();
        ctx.fill();
        ctx.stroke();

        // Tab text
        ctx.fillStyle = COLORS.text;
        ctx.textAlign = 'left';
        ctx.textBaseline = 'middle';
        ctx.fillText(tabText, tabX + 7, tabY + TAB_HEIGHT / 2);

        // Content padding
        const contentX = x + 10;
        const contentY = y + 14;
        const lineHeight = 16;

        // Line 1: Mesh IP + version
        ctx.fillStyle = COLORS.text;
        ctx.font = '12px monospace';
        ctx.textAlign = 'left';
        ctx.fillText(node.meshIP, contentX, contentY + 4);

        // Version (right aligned)
        if (node.version) {
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '10px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'right';
            ctx.fillText(node.version, x + CARD_WIDTH - 10, contentY + 4);
        }

        // Line 2: Tunnel count
        ctx.fillStyle = COLORS.textDim;
        ctx.font = '11px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        ctx.textAlign = 'left';
        const tunnelText = node.activeTunnels === 1 ? '1 tunnel' : `${node.activeTunnels} tunnels`;
        ctx.fillText(tunnelText, contentX, contentY + lineHeight + 6);
    }

}

// Export for use in app.js
window.NodeVisualizer = NodeVisualizer;
window.NodeType = NodeType;
