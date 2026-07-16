// Horde control-panel event handlers.
//
// These back the buttons in app.xml. They call the horde node HTTP API with
// the synchronous http_* helpers and report progress on the status line.
//
// NOTE: keep api_base() in sync with the `api_base` variable in app.xml.

// Base URL of the horde node API.
fn api_base() {
    "http://localhost:13420/api/v1"
}

// Update the status line at the top of the window.
fn set_status(msg) {
    set_component_text("status_line", msg);
}

// Report the outcome of an http_* call on the status line. Returns true on
// success so callers can branch. `what` names the action for the message.
fn report(resp, what) {
    if "error" in resp {
        set_status(what + " failed: " + resp.error);
        return false;
    }
    if resp.ok {
        set_status(what + " succeeded.");
        return true;
    }
    set_status(what + " failed (HTTP " + to_string(resp.status) + ").");
    return false;
}

// Read a component's current value, trimmed. Returns "" when unset.
fn field(id) {
    let v = get_component_property(id, "value");
    if v == () {
        return "";
    }
    trim(to_string(v))
}

// on-load: seed the status line so the user knows where we point. Nemo invokes
// every handler with (component_id, event_data), so this must take two params
// even though it ignores them — Rhai matches functions by name AND arity.
fn on_load(component_id, event_data) {
    set_status("Ready. Node API: " + api_base());
}

// ---- Agents ---------------------------------------------------------------

fn create_agent(component_id, event_data) {
    let name = field("new_agent_name");
    if name == "" {
        set_status("Enter an agent name first.");
        return;
    }
    let resp = http_post(api_base() + "/agents", json_stringify(#{ name: name }));
    if report(resp, "Start agent \"" + name + "\"") {
        set_component_property("new_agent_name", "value", "");
    }
}

// ---- Projects -------------------------------------------------------------

fn create_project(component_id, event_data) {
    let name = field("new_project_name");
    let workspace = field("new_project_workspace");
    let goal = field("new_project_goal");
    if name == "" || workspace == "" {
        set_status("A project needs at least a name and a workspace path.");
        return;
    }
    let payload = json_stringify(#{
        name: name,
        workspace: workspace,
        goal: goal,
    });
    let resp = http_post(api_base() + "/projects", payload);
    if report(resp, "Create project \"" + name + "\"") {
        set_component_property("new_project_name", "value", "");
        set_component_property("new_project_workspace", "value", "");
        set_component_property("new_project_goal", "value", "");
    }
}

// Shared helper for the pause/resume/finish state transitions.
fn transition_project(action) {
    let id = field("manage_project_id");
    if id == "" {
        set_status("Enter a project ID first.");
        return;
    }
    let resp = http_post(api_base() + "/projects/" + id + "/" + action, "");
    report(resp, to_upper(action) + " project " + id);
}

fn pause_project(component_id, event_data) {
    transition_project("pause");
}

fn resume_project(component_id, event_data) {
    transition_project("resume");
}

fn finish_project(component_id, event_data) {
    transition_project("finish");
}

// ---- Talk to an agent -----------------------------------------------------

fn send_task(component_id, event_data) {
    let id = field("invoke_agent_id");
    let msg = field("task_message");
    if id == "" {
        set_status("Enter an agent ID first.");
        return;
    }
    if msg == "" {
        set_status("Type a task message first.");
        return;
    }
    set_status("Sending task to agent " + id + "…");
    let resp = http_post(
        api_base() + "/agents/" + id + "/invoke",
        json_stringify(#{ message: msg })
    );
    if "error" in resp {
        set_status("Task failed: " + resp.error);
        set_component_property("task_result", "value", resp.error);
        return;
    }
    // The invoke endpoint streams Server-Sent Events; http_post returns the
    // whole stream body once the turn completes. Show it verbatim.
    set_component_property("task_result", "value", resp.body);
    report(resp, "Task");
}
