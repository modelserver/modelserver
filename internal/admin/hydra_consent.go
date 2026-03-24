package admin

import (
	"embed"
	"html/template"
	"log"
	"net/http"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

//go:embed templates/consent.html
var consentTemplateFS embed.FS

// ConsentHandler handles Hydra's consent provider endpoint.
type ConsentHandler struct {
	hydra     *HydraClient
	store     *store.Store
	templates *template.Template
}

// consentProject pairs a project with the user's role in it.
type consentProject struct {
	Project types.Project
	Role    string
}

// consentTemplateData is the data passed to the consent HTML template.
type consentTemplateData struct {
	Challenge string
	ClientID  string
	Projects  []consentProject
	Error     string
}

// NewConsentHandler constructs a ConsentHandler and parses the embedded template.
// Returns an error if the template cannot be parsed.
func NewConsentHandler(hydra *HydraClient, st *store.Store) (*ConsentHandler, error) {
	tmpl, err := template.ParseFS(consentTemplateFS, "templates/consent.html")
	if err != nil {
		return nil, err
	}
	return &ConsentHandler{
		hydra:     hydra,
		store:     st,
		templates: tmpl,
	}, nil
}

// ServeHTTP dispatches to the appropriate method handler based on the HTTP method.
func (h *ConsentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGet handles GET /oauth/consent?consent_challenge=...
//
// Flow:
//  1. Get the consent_challenge from query parameters.
//  2. Fetch the consent request from Hydra to get the subject and client info.
//  3. List the user's projects.
//  4. For each project, fetch the user's role via GetProjectMember.
//  5. Render the consent page with the project list.
func (h *ConsentHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	challenge := r.URL.Query().Get("consent_challenge")
	if challenge == "" {
		http.Error(w, "missing consent_challenge", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Step 2: Fetch consent request from Hydra.
	consentReq, err := h.hydra.GetConsentRequest(ctx, challenge)
	if err != nil {
		log.Printf("ERROR hydra_consent: GetConsentRequest challenge=%s: %v", challenge, err)
		h.renderError(w, challenge, "", "Failed to fetch consent request. Please try again.")
		return
	}

	subject := consentReq.Subject
	clientID := consentReq.Client.ClientID

	// Step 3: List the user's projects.
	projects, _, err := h.store.ListUserProjects(subject, types.PaginationParams{
		Page:    1,
		PerPage: 100,
		Sort:    "created_at",
		Order:   "desc",
	})
	if err != nil {
		log.Printf("ERROR hydra_consent: ListUserProjects subject=%s: %v", subject, err)
		h.renderError(w, challenge, clientID, "Failed to load your projects. Please try again.")
		return
	}

	// Step 4: Fetch the user's role in each project.
	var consentProjects []consentProject
	for _, proj := range projects {
		member, err := h.store.GetProjectMember(proj.ID, subject)
		if err != nil {
			log.Printf("WARN hydra_consent: GetProjectMember project=%s subject=%s: %v", proj.ID, subject, err)
			continue
		}
		role := ""
		if member != nil {
			role = member.Role
		}
		consentProjects = append(consentProjects, consentProject{
			Project: proj,
			Role:    role,
		})
	}

	// Step 5: Render the consent page.
	h.render(w, consentTemplateData{
		Challenge: challenge,
		ClientID:  clientID,
		Projects:  consentProjects,
	})
}

// handlePost handles POST /oauth/consent.
//
// Flow:
//  1. Parse the form fields: consent_challenge and project_id.
//  2. Fetch the consent request from Hydra to get the subject.
//  3. Verify the user is a member of the selected project.
//  4. Fetch project details.
//  5. Accept the consent with session data embedding the project context.
//  6. Redirect the user to the Hydra-provided redirect URL.
func (h *ConsentHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	challenge := r.FormValue("consent_challenge")
	projectID := r.FormValue("project_id")

	if challenge == "" || projectID == "" {
		http.Error(w, "missing consent_challenge or project_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Step 2: Fetch consent request from Hydra to get the subject.
	consentReq, err := h.hydra.GetConsentRequest(ctx, challenge)
	if err != nil {
		log.Printf("ERROR hydra_consent: GetConsentRequest (POST) challenge=%s: %v", challenge, err)
		h.renderError(w, challenge, "", "Failed to verify consent request. Please try again.")
		return
	}

	subject := consentReq.Subject
	clientID := consentReq.Client.ClientID

	// Step 3: Verify the user is a member of the selected project.
	member, err := h.store.GetProjectMember(projectID, subject)
	if err != nil {
		log.Printf("ERROR hydra_consent: GetProjectMember project=%s subject=%s: %v", projectID, subject, err)
		h.renderError(w, challenge, clientID, "Failed to verify project access. Please try again.")
		return
	}
	if member == nil {
		log.Printf("WARN hydra_consent: unauthorized project selection project=%s subject=%s", projectID, subject)
		h.renderError(w, challenge, clientID, "You do not have access to the selected project.")
		return
	}

	// Step 4: Fetch project details.
	project, err := h.store.GetProjectByID(projectID)
	if err != nil {
		log.Printf("ERROR hydra_consent: GetProjectByID project=%s: %v", projectID, err)
		h.renderError(w, challenge, clientID, "Failed to load project details. Please try again.")
		return
	}
	if project == nil {
		h.renderError(w, challenge, clientID, "Project not found.")
		return
	}

	// Step 5: Accept the consent with embedded project context in the access token.
	sessionData := map[string]interface{}{
		"project_id":   project.ID,
		"project_name": project.Name,
		"user_id":      subject,
	}

	redirect, err := h.hydra.AcceptConsent(ctx, challenge, consentReq.RequestedScope, sessionData)
	if err != nil {
		log.Printf("ERROR hydra_consent: AcceptConsent challenge=%s project=%s subject=%s: %v", challenge, projectID, subject, err)
		h.renderError(w, challenge, clientID, "Failed to complete authorization. Please try again.")
		return
	}

	// Record the OAuth grant so project owners can see and revoke it later.
	grant := &types.OAuthGrant{
		ProjectID: project.ID,
		UserID:    subject,
		ClientID:  consentReq.Client.ClientID,
		Scopes:    consentReq.RequestedScope,
	}
	if err := h.store.CreateOAuthGrant(grant); err != nil {
		log.Printf("WARN hydra_consent: failed to record grant: %v", err)
		// Non-fatal: don't block the consent flow.
	}

	// Step 6: Redirect to the Hydra-provided URL.
	http.Redirect(w, r, redirect.RedirectTo, http.StatusFound)
}

// render executes the consent template with the given data.
func (h *ConsentHandler) render(w http.ResponseWriter, data consentTemplateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "consent.html", data); err != nil {
		log.Printf("ERROR hydra_consent: render template: %v", err)
	}
}

// renderError renders the consent page with an error message.
func (h *ConsentHandler) renderError(w http.ResponseWriter, challenge, clientID, errMsg string) {
	h.render(w, consentTemplateData{
		Challenge: challenge,
		ClientID:  clientID,
		Error:     errMsg,
	})
}
