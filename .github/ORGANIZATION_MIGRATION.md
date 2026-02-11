# Organization Migration Plan

## Current State

TunnelMesh currently has **5 user-level projects** (at `github.com/users/zombar/projects/*`):

- Project 9: Security Issues (23 issues)
- Project 10: Docker & Data Views (2 issues)
- Project 11: Monitoring & Observability (15 issues)
- Project 12: Technical Debt & Refactoring (21 issues)
- Project 13: Critical & High Priority (13 issues)

**⚠️ CRITICAL**: User-level projects do NOT transfer when migrating a repository to an organization.
Project links will break and contributors won't have access.

## Migration Strategy

### Phase 1: Create Organization

1. Create new GitHub organization (e.g., `tunnelmesh-org`)
2. Configure organization settings:
   - Enable Projects V2
   - Set default permissions for members
   - Configure team access

### Phase 2: Transfer Repository

```bash
# Transfer via GitHub UI
# Settings → Danger Zone → Transfer ownership
# Select your new organization
```

**Note**: Issues, PRs, labels, and milestones transfer automatically. Project associations do NOT.

### Phase 3: Recreate Projects at Organization Level

Manually recreate projects using the GitHub CLI or web interface. See project structure details below.

## Project Structure to Recreate

### Project 1: Security Issues

**Purpose**: Track all security vulnerabilities and security enhancements

**Issues to add**:

- CRITICAL: #296, #197, #196
- HIGH: #201, #200, #199, #198, #217
- MEDIUM: #206, #205, #204, #203, #202, #190
- LOW: #210, #209, #208, #207
- Enhancement: #211, #220, #212, #298

### Project 2: Critical & High Priority

**Purpose**: Sprint planning board for urgent items only

**Issues to add**:

- CRITICAL: #296, #197, #196, #292
- HIGH: #297, #201, #200, #199, #198, #220, #219, #218, #217

### Project 3: Monitoring & Observability

**Purpose**: Track observability improvements

**Issues to add**: #295, #233, #218, #219, #220, #221, #222, #223, #224, #225, #226, #227, #228, #217

### Project 4: Technical Debt & Refactoring

**Purpose**: Code quality and architectural improvements

**Issues to add**:

- Refactoring: #282, #283, #284, #285, #286, #287
- Dependencies: #296, #299, #300, #301
- Code quality: #289, #290, #291, #294
- Documentation: #262, #263, #264, #265, #281, #189, #191

### Project 5: Docker & Data Views

**Purpose**: Docker integration and data panel features

**Issues to add**: #297, #281

## Creating Projects at Organization Level

After transferring the repository, recreate the projects using GitHub CLI:

```bash
# Create each project
gh project create --owner YOUR_ORG_NAME --title "TunnelMesh: Security Issues"
gh project create --owner YOUR_ORG_NAME --title "TunnelMesh: Critical & High Priority"
gh project create --owner YOUR_ORG_NAME --title "TunnelMesh: Monitoring & Observability"
gh project create --owner YOUR_ORG_NAME --title "TunnelMesh: Technical Debt & Refactoring"
gh project create --owner YOUR_ORG_NAME --title "TunnelMesh: Docker & Data Views"

# Add issues to projects (example for Security Issues)
gh project item-add PROJECT_NUMBER --owner YOUR_ORG_NAME \
    --url "https://github.com/YOUR_ORG_NAME/tunnelmesh/issues/296"
```

Or use the GitHub web interface:

1. Go to `https://github.com/orgs/YOUR_ORG_NAME/projects`
2. Click "New project"
3. Add issues by searching for issue numbers

## Migration Checklist

- [ ] Create GitHub organization
- [ ] Configure organization settings and permissions
- [ ] Transfer repository to organization (Settings → Transfer ownership)
- [ ] Recreate 5 projects at organization level (see project structure above)
- [ ] Add issues to each project using GitHub CLI or web interface
- [ ] Verify all projects created successfully
- [ ] Configure project views and custom fields as needed
- [ ] Grant team members appropriate project access
- [ ] Archive old user-level projects (optional)

## Access Control After Migration

Organization-level projects support:

- **Team-based access**: Assign teams to projects
- **Granular permissions**: Read, Write, Admin per team/user
- **Public/Private visibility**: Control who can see projects

Recommended structure:

- **Maintainers team**: Admin access to all projects
- **Contributors team**: Write access to Technical Debt, Read access to Security
- **Triagers team**: Write access to all projects for issue management

## Important Notes

1. **Labels transfer automatically** with the repository
2. **Issue numbers remain the same** after transfer
3. **Project associations are lost** and must be recreated
4. **Webhooks, Actions, secrets** need to be reconfigured at org level
5. **GitHub Pages** URL will change if enabled

## After Migration

Update documentation references:

- Replace `github.com/zombar/tunnelmesh` with `github.com/YOUR_ORG/tunnelmesh`
- Update project URLs in README
- Update contributor guidelines
- Update CI/CD configuration if repo-specific
