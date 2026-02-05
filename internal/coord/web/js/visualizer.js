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

const CARD_WIDTH = 210;
const CARD_HEIGHT = 80;
const TAB_HEIGHT = 22;
const ROW_SPACING = 110;  // Vertical spacing between spread nodes
const CONNECTION_DOT_RADIUS = 5;
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

        // Throughput stats
        this.bytesSentRate = peer.bytes_sent_rate || 0;
        this.bytesReceivedRate = peer.bytes_received_rate || 0;

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

// Format bytes rate compactly
function formatBytesCompact(bytes) {
    if (bytes < 1024) return bytes + 'B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + 'K';
    return (bytes / (1024 * 1024)).toFixed(1) + 'M';
}

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
// Visual Slot - represents a node's position on one side (left or right)
// =============================================================================

class VisualSlot {
    constructor(node, side) {
        this.node = node;
        this.side = side;  // 'left', 'center', or 'right'
        this.x = 0;
        this.y = 0;
        this.startX = 0;
        this.startY = 0;
        this.targetX = 0;
        this.targetY = 0;
        this.visible = true;
    }

    get id() { return `${this.node.id}_${this.side}`; }
}

// =============================================================================
// Layout Algorithm
// =============================================================================

function calculateLayout(nodes, selectedId, canvasWidth, canvasHeight, stackInfo, slots) {
    const centerX = canvasWidth / 2;
    const centerY = canvasHeight / 2;

    // Dynamic column spacing based on canvas width (adjust to fit)
    const columnSpacing = Math.min(300, (canvasWidth - CARD_WIDTH * 3) / 2.5);

    // Reset stack info
    stackInfo.left = { total: 0, hidden: 0 };
    stackInfo.right = { total: 0, hidden: 0 };
    stackInfo.columnSpacing = columnSpacing;

    // Clear old slots
    slots.length = 0;

    if (!selectedId || !nodes.has(selectedId)) {
        return;
    }

    const selectedNode = nodes.get(selectedId);

    // Create center slot for selected node
    const centerSlot = new VisualSlot(selectedNode, 'center');
    centerSlot.targetX = centerX;
    centerSlot.targetY = centerY;
    slots.push(centerSlot);

    // Bidirectional mesh visualization:
    // Left: nodes that can reach selected (incoming)
    // Right: nodes that selected can reach (outgoing)
    const incoming = [];
    const outgoing = [];

    for (const [id, node] of nodes) {
        if (id === selectedId) continue;
        if (!node.online) continue;

        if (canNodeReach(node, selectedNode)) {
            incoming.push(node);
        }
        if (canNodeReach(selectedNode, node)) {
            outgoing.push(node);
        }
    }

    // Sort consistently
    const sortByName = (a, b) => a.name.localeCompare(b.name);
    incoming.sort(sortByName);
    outgoing.sort(sortByName);

    // Create slots for each column
    layoutColumn(incoming, centerX - columnSpacing, centerY, stackInfo.left, slots, 'left');
    layoutColumn(outgoing, centerX + columnSpacing, centerY, stackInfo.right, slots, 'right');
}

function layoutColumn(nodes, centerX, centerY, stackInfo, slots, side) {
    if (nodes.length === 0) return;

    stackInfo.total = nodes.length;

    const visibleCount = Math.min(nodes.length, MAX_VISIBLE_NODES);
    stackInfo.hidden = nodes.length - visibleCount;

    const totalHeight = (visibleCount - 1) * ROW_SPACING;
    let currentY = centerY - totalHeight / 2;

    for (let i = 0; i < visibleCount; i++) {
        const node = nodes[i];
        const slot = new VisualSlot(node, side);
        slot.targetX = centerX;
        slot.targetY = currentY;
        slots.push(slot);
        currentY += ROW_SPACING;
    }
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
        this.slots = [];  // Visual slots for rendering
        this.selectedNodeId = null;
        this.hoveredSlotId = null;
        this.domainSuffix = '.tunnelmesh';

        // Stack info for "+ N more" labels
        this.stackInfo = {
            left: { total: 0, hidden: 0 },
            right: { total: 0, hidden: 0 }
        };

        // Animation
        this.animating = false;
        this.animationStart = 0;
        this.animationDuration = 400;  // ms

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
                node.bytesSentRate = peer.bytes_sent_rate || 0;
                node.bytesReceivedRate = peer.bytes_received_rate || 0;
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
        const oldSlots = new Map(this.slots.map(s => [s.id, { x: s.x, y: s.y }]));

        calculateLayout(this.nodes, this.selectedNodeId, rect.width, rect.height, this.stackInfo, this.slots);

        // Preserve old positions for animation, or initialize to target
        for (const slot of this.slots) {
            const old = oldSlots.get(slot.id);
            if (old) {
                slot.startX = old.x;
                slot.startY = old.y;
                slot.x = old.x;
                slot.y = old.y;
            } else {
                // New slot - start at target (no animation)
                slot.startX = slot.targetX;
                slot.startY = slot.targetY;
                slot.x = slot.targetX;
                slot.y = slot.targetY;
            }
        }
    }

    // -------------------------------------------------------------------------
    // Animation
    // -------------------------------------------------------------------------

    startAnimation() {
        // Capture start positions
        for (const slot of this.slots) {
            slot.startX = slot.x;
            slot.startY = slot.y;
        }

        this.animationStart = performance.now();
        if (!this.animating) {
            this.animating = true;
            this.animate();
        }
    }

    animate() {
        const elapsed = performance.now() - this.animationStart;
        const progress = Math.min(elapsed / this.animationDuration, 1);

        // Ease out cubic
        const t = 1 - Math.pow(1 - progress, 3);

        // Interpolate positions
        for (const slot of this.slots) {
            slot.x = slot.startX + (slot.targetX - slot.startX) * t;
            slot.y = slot.startY + (slot.targetY - slot.startY) * t;
        }

        this.render();

        if (progress < 1) {
            requestAnimationFrame(() => this.animate());
        } else {
            this.animating = false;
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
        const slot = this.hitTestSlots(x, y);
        const newHoveredId = slot ? slot.id : null;

        if (newHoveredId !== this.hoveredSlotId) {
            this.hoveredSlotId = newHoveredId;
            this.canvas.style.cursor = newHoveredId ? 'pointer' : 'default';
            this.render();
        }
    }

    onMouseLeave() {
        this.hoveredSlotId = null;
        this.canvas.style.cursor = 'default';
        this.render();
    }

    onClick(e) {
        const rect = this.canvas.getBoundingClientRect();
        const x = e.clientX - rect.left;
        const y = e.clientY - rect.top;

        const slot = this.hitTestSlots(x, y);
        if (slot && slot.node.id !== this.selectedNodeId) {
            this.selectNode(slot.node.id);
        }
    }

    hitTestSlots(screenX, screenY) {
        // Check slots in reverse order (topmost first)
        for (let i = this.slots.length - 1; i >= 0; i--) {
            const slot = this.slots[i];
            const halfWidth = CARD_WIDTH / 2;
            const halfHeight = CARD_HEIGHT / 2;

            if (screenX >= slot.x - halfWidth && screenX <= slot.x + halfWidth &&
                screenY >= slot.y - halfHeight - TAB_HEIGHT && screenY <= slot.y + halfHeight) {
                return slot;
            }
        }
        return null;
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
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '14px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'center';
            ctx.fillText('No peers connected', width / 2, height / 2);
            return;
        }

        if (!this.selectedNodeId) {
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '14px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'center';
            ctx.fillText('Select a peer to view connections', width / 2, height / 2);
            return;
        }

        // Draw connections first (behind nodes)
        this.renderConnections(ctx);

        // Draw slots (center slot last so it's on top)
        const sortedSlots = [...this.slots].sort((a, b) => {
            if (a.side === 'center') return 1;
            if (b.side === 'center') return -1;
            return 0;
        });

        for (const slot of sortedSlots) {
            const isHovered = slot.id === this.hoveredSlotId;
            this.renderSlot(ctx, slot, isHovered);
        }

        // Draw "+ N more" labels
        this.renderStackLabels(ctx, width, height);
    }

    renderStackLabels(ctx, width, height) {
        const centerSlot = this.slots.find(s => s.side === 'center');
        if (!centerSlot) return;

        const columnSpacing = this.stackInfo.columnSpacing || 280;

        ctx.font = '12px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        ctx.fillStyle = COLORS.textDim;

        // Left side label
        if (this.stackInfo.left.hidden > 0) {
            ctx.textAlign = 'center';
            const labelX = centerSlot.x - columnSpacing;
            const labelY = height - 20;
            ctx.fillText(`+ ${this.stackInfo.left.hidden} more`, labelX, labelY);
        }

        // Right side label
        if (this.stackInfo.right.hidden > 0) {
            ctx.textAlign = 'center';
            const labelX = centerSlot.x + columnSpacing;
            const labelY = height - 20;
            ctx.fillText(`+ ${this.stackInfo.right.hidden} more`, labelX, labelY);
        }
    }

    renderConnections(ctx) {
        const centerSlot = this.slots.find(s => s.side === 'center');
        if (!centerSlot) return;

        for (const slot of this.slots) {
            if (slot.side === 'center') continue;

            const isLeft = slot.side === 'left';

            ctx.beginPath();
            ctx.strokeStyle = COLORS.connectionHighlight;
            ctx.lineWidth = 2;

            const startX = isLeft ? slot.x + CARD_WIDTH / 2 : slot.x - CARD_WIDTH / 2;
            const startY = slot.y;
            const endX = isLeft ? centerSlot.x - CARD_WIDTH / 2 : centerSlot.x + CARD_WIDTH / 2;
            const endY = centerSlot.y;

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

    renderSlot(ctx, slot, isHovered) {
        const node = slot.node;
        const x = slot.x - CARD_WIDTH / 2;
        const y = slot.y - CARD_HEIGHT / 2;

        const tabText = node.name;
        ctx.font = 'bold 11px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        const tabWidth = ctx.measureText(tabText).width + 14;
        const tabX = x;
        const tabY = y - TAB_HEIGHT;

        // Draw combined tab + card shape
        ctx.fillStyle = isHovered ? '#2d333b' : COLORS.cardFill;
        ctx.strokeStyle = isHovered ? '#58a6ff' : COLORS.cardStroke;
        ctx.lineWidth = isHovered ? 2 : 1;

        ctx.beginPath();
        ctx.moveTo(tabX, y);
        ctx.lineTo(tabX, tabY + 4);
        ctx.arcTo(tabX, tabY, tabX + 4, tabY, 4);
        ctx.lineTo(tabX + tabWidth - 4, tabY);
        ctx.arcTo(tabX + tabWidth, tabY, tabX + tabWidth, tabY + 4, 4);
        ctx.lineTo(tabX + tabWidth, y);
        ctx.lineTo(x + CARD_WIDTH - 6, y);
        ctx.arcTo(x + CARD_WIDTH, y, x + CARD_WIDTH, y + 6, 6);
        ctx.lineTo(x + CARD_WIDTH, y + CARD_HEIGHT - 6);
        ctx.arcTo(x + CARD_WIDTH, y + CARD_HEIGHT, x + CARD_WIDTH - 6, y + CARD_HEIGHT, 6);
        ctx.lineTo(x + 6, y + CARD_HEIGHT);
        ctx.arcTo(x, y + CARD_HEIGHT, x, y + CARD_HEIGHT - 6, 6);
        ctx.lineTo(x, y);
        ctx.closePath();
        ctx.fill();
        ctx.stroke();

        // Tab text
        ctx.fillStyle = COLORS.text;
        ctx.textAlign = 'left';
        ctx.textBaseline = 'middle';
        ctx.fillText(tabText, tabX + 7, tabY + TAB_HEIGHT / 2);

        // Content
        const contentX = x + 10;
        const contentY = y + 14;
        const lineHeight = 16;

        ctx.fillStyle = COLORS.text;
        ctx.font = '12px monospace';
        ctx.textAlign = 'left';
        ctx.fillText(node.meshIP, contentX, contentY + 4);

        if (node.version) {
            ctx.fillStyle = COLORS.textDim;
            ctx.font = '10px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
            ctx.textAlign = 'right';
            ctx.fillText(node.version, x + CARD_WIDTH - 10, contentY + 4);
        }

        ctx.fillStyle = COLORS.textDim;
        ctx.font = '11px -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif';
        ctx.textAlign = 'left';
        const tunnelText = `${node.activeTunnels}t`;
        ctx.fillText(tunnelText, contentX, contentY + lineHeight + 6);

        // Throughput (right side of line 2)
        const throughputText = `↑${formatBytesCompact(node.bytesSentRate)} ↓${formatBytesCompact(node.bytesReceivedRate)}`;
        ctx.textAlign = 'right';
        ctx.fillText(throughputText, x + CARD_WIDTH - 10, contentY + lineHeight + 6);
    }

}

// Export for use in app.js
window.NodeVisualizer = NodeVisualizer;
window.NodeType = NodeType;
