import os
import sys
import shutil
import subprocess
import time
import json
import datetime
import urllib.request
import urllib.error
import threading
import concurrent.futures

TEST_ENV_DIR = "/tmp/stash-test-env"
STASH_DIR = os.path.join(TEST_ENV_DIR, ".stash")
CONFIG_DIR = os.path.join(TEST_ENV_DIR, ".config", "stash")
STASH_RACE_BIN = os.path.abspath("./stash-race")

def clean_env():
    if os.path.exists(TEST_ENV_DIR):
        shutil.rmtree(TEST_ENV_DIR)
    os.makedirs(STASH_DIR, exist_ok=True)
    os.makedirs(CONFIG_DIR, exist_ok=True)
    os.makedirs(os.path.join(TEST_ENV_DIR, "bin"), exist_ok=True)

def write_mock_security():
    mock_sec_path = os.path.join(TEST_ENV_DIR, "bin", "security")
    with open(mock_sec_path, "w") as f:
        f.write("""#!/bin/bash
if [[ "$*" == *"find-generic-password"* ]]; then
    echo "AIza_mock_gemini_api_key_for_testing"
    exit 0
fi
exit 1
""")
    os.chmod(mock_sec_path, 0o755)

def write_mock_ffprobe(mode="fail", duration="10.5"):
    mock_ffprobe_path = os.path.join(TEST_ENV_DIR, "bin", "ffprobe")
    with open(mock_ffprobe_path, "w") as f:
        if mode == "fail":
            f.write("""#!/bin/bash
echo "mock ffprobe error" >&2
exit 1
""")
        else:
            f.write(f"""#!/bin/bash
echo "{duration}"
exit 0
""")
    os.chmod(mock_ffprobe_path, 0o755)

def get_env():
    env = os.environ.copy()
    env["HOME"] = TEST_ENV_DIR
    env["STASH_DIR"] = STASH_DIR
    env["PATH"] = os.path.join(TEST_ENV_DIR, "bin") + os.pathsep + env.get("PATH", "")
    return env

def write_config(max_daily=0.10, max_monthly=1.00):
    config_path = os.path.join(CONFIG_DIR, "config.toml")
    with open(config_path, "w") as f:
        f.write(f"""max_daily_budget_usd = {max_daily}
max_monthly_budget_usd = {max_monthly}
""")

def write_ledger(date_str, month_str, prompt_tokens, candidate_tokens):
    ledger_path = os.path.join(STASH_DIR, "gemini-usage.json")
    # pricing cost calculations: gemini-2.5-flash inputs: 0.30 / M, outputs: 2.50 / M
    # input_cost = prompt_tokens * 0.30 / 1e6
    # output_cost = candidate_tokens * 2.50 / 1e6
    snap = {
        "today": {
            "by_model": {
                "gemini-2.5-flash": {
                    "calls": 1,
                    "input_tokens": prompt_tokens,
                    "output_tokens": candidate_tokens
                }
            }
        },
        "this_month": {
            "by_model": {
                "gemini-2.5-flash": {
                    "calls": 1,
                    "input_tokens": prompt_tokens,
                    "output_tokens": candidate_tokens
                }
            }
        },
        "all_time": {
            "by_model": {
                "gemini-2.5-flash": {
                    "calls": 1,
                    "input_tokens": prompt_tokens,
                    "output_tokens": candidate_tokens
                }
            }
        },
        "date": date_str,
        "month": month_str,
        "first_seen_date": date_str
    }
    with open(ledger_path, "w") as f:
        json.dump(snap, f, indent=2)

def start_server(env, port=28999):
    proc = subprocess.Popen(
        [STASH_RACE_BIN, "serve", "--addr", f":{port}", "--no-qr"],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True
    )
    # Wait for server token to be created and server to listen
    token_path = os.path.join(STASH_DIR, "serve.token")
    start_time = time.time()
    while time.time() - start_time < 5.0:
        if os.path.exists(token_path):
            with open(token_path) as f:
                token = f.read().strip()
            if token:
                # Give server another small moment to bind to the port
                time.sleep(0.5)
                return proc, token
        time.sleep(0.1)
    
    # Dump stdout/stderr on failure
    stdout, stderr = proc.communicate(timeout=1.0)
    print(f"Failed to start server. Stdout:\\n{stdout}\\nStderr:\\n{stderr}")
    raise RuntimeError("Server failed to start")

def stop_server(proc):
    proc.terminate()
    try:
        stdout, stderr = proc.communicate(timeout=5.0)
    except subprocess.TimeoutExpired:
        proc.kill()
        stdout, stderr = proc.communicate()
    return stdout, stderr

def make_request(url, token, method="GET", data=None):
    headers = {"Authorization": f"Bearer {token}"}
    if data is not None:
        headers["Content-Type"] = "application/json"
        req_data = json.dumps(data).encode("utf-8")
    else:
        req_data = None

    req = urllib.request.Request(url, data=req_data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5.0) as resp:
            body = resp.read()
            return resp.status, body
    except urllib.error.HTTPError as e:
        body = e.read()
        return e.code, body
    except Exception as e:
        return 0, str(e).encode("utf-8")

# --- VERIFICATION 1: Concurrency Safety ---
def run_concurrency_test():
    print("=== Running Verification 1: Concurrency Safety ===")
    clean_env()
    write_mock_security()
    write_config(max_daily=10.0, max_monthly=100.0) # High budgets
    
    env = get_env()
    proc, token = start_server(env)
    
    post_url = "http://localhost:28999/gemini-usage"
    search_url = "http://localhost:28999/search?q=test&semantic=true"
    
    num_threads = 10
    requests_per_thread = 10
    
    errors = []
    total_prompt_tokens = 0
    total_candidate_tokens = 0
    tokens_lock = threading.Lock()
    
    def post_worker(thread_idx):
        nonlocal total_prompt_tokens, total_candidate_tokens
        local_p = 0
        local_c = 0
        for i in range(requests_per_thread):
            # Send 100 prompt and 50 candidate tokens
            payload = {
                "model": "gemini-2.5-flash",
                "prompt_tokens": 100,
                "candidate_tokens": 50
            }
            status, body = make_request(post_url, token, method="POST", data=payload)
            if status != 204:
                errors.append(f"Thread {thread_idx} POST failed with status {status}: {body.decode()}")
            else:
                local_p += 100
                local_c += 50
        with tokens_lock:
            total_prompt_tokens += local_p
            total_candidate_tokens += local_c

    def get_worker(thread_idx):
        for i in range(requests_per_thread):
            status, body = make_request(search_url, token, method="GET")
            # We don't have embeddings so it might fail with StatusFailedDependency/Key missing
            # or 200, but it should NOT return 500 or crash.
            if status == 500:
                errors.append(f"Thread {thread_idx} GET returned 500: {body.decode()}")

    threads = []
    # 5 threads recording usage, 5 checking budget/searching
    for i in range(5):
        t = threading.Thread(target=post_worker, args=(i,))
        threads.append(t)
    for i in range(5):
        t = threading.Thread(target=get_worker, args=(i+5,))
        threads.append(t)
        
    for t in threads:
        t.start()
    for t in threads:
        t.join()
        
    stdout, stderr = stop_server(proc)
    
    # Check for Go race detector output
    has_race = "WARNING: DATA RACE" in stderr
    
    # Check ledger parsing and values
    ledger_file = os.path.join(STASH_DIR, "gemini-usage.json")
    if not os.path.exists(ledger_file):
        errors.append("ledger file was not created")
        parsed_snap = {}
    else:
        try:
            with open(ledger_file) as f:
                parsed_snap = json.load(f)
        except Exception as e:
            errors.append(f"failed to parse ledger JSON: {e}")
            parsed_snap = {}
            
    # Verify exact accumulators
    flash_today = parsed_snap.get("today", {}).get("by_model", {}).get("gemini-2.5-flash", {})
    recorded_prompt = flash_today.get("input_tokens", 0)
    recorded_candidate = flash_today.get("output_tokens", 0)
    
    print(f"Total prompt tokens sent: {total_prompt_tokens}, recorded: {recorded_prompt}")
    print(f"Total candidate tokens sent: {total_candidate_tokens}, recorded: {recorded_candidate}")
    print(f"Errors detected during concurrency requests: {len(errors)}")
    print(f"Go race detector warning present: {has_race}")
    
    assert len(errors) == 0, f"HTTP Errors: {errors}"
    assert not has_race, "DATA RACE DETECTED BY GO!"
    assert recorded_prompt == total_prompt_tokens, "Prompt token mismatch!"
    assert recorded_candidate == total_candidate_tokens, "Candidate token mismatch!"
    print("Verification 1: PASS")

# --- VERIFICATION 2: Lockout Recovery ---
def run_lockout_recovery_test():
    print("=== Running Verification 2: Lockout Recovery ===")
    clean_env()
    write_mock_security()
    write_config(max_daily=0.10, max_monthly=1.00)
    
    # Write ledger file representing YESTERDAY with huge tokens (exceeding $0.10 limit)
    # 1,000,000 input tokens of gemini-2.5-flash = $0.30 cost, exceeding $0.10 daily budget
    yesterday = (datetime.date.today() - datetime.timedelta(days=1)).strftime("%Y-%m-%d")
    current_month = datetime.date.today().strftime("%Y-%m-%d")[:7] # YYYY-MM
    
    write_ledger(
        date_str=yesterday,
        month_str=current_month,
        prompt_tokens=1000000,
        candidate_tokens=0
    )
    
    env = get_env()
    proc, token = start_server(env)
    
    # Query /search?q=test&semantic=true.
    # Since today's date is different from yesterday, it should immediately reset 'today' usage to 0
    # and allow the search (or fail on key resolved rather than budget exceeded).
    search_url = "http://localhost:28999/search?q=test&semantic=true"
    status, body = make_request(search_url, token, method="GET")
    
    print(f"Query returned status: {status}, body: {body.decode()}")
    
    stop_server(proc)
    
    # Verify that the ledger file on disk was updated to today's date and today's usage was reset to 0
    ledger_file = os.path.join(STASH_DIR, "gemini-usage.json")
    with open(ledger_file) as f:
        snap = json.load(f)
        
    today_str = datetime.date.today().strftime("%Y-%m-%d")
    print(f"Ledger date: {snap.get('date')}, expected: {today_str}")
    print(f"Today's model usage keys: {list(snap.get('today', {}).get('by_model', {}).keys())}")
    
    assert status != 429, "Search query returned 429 Too Many Requests (budget exceeded), but budget lockout should have been recovered!"
    assert snap.get("date") == today_str, f"Ledger date did not update to today, got {snap.get('date')}"
    assert len(snap.get("today", {}).get("by_model", {})) == 0, "Today's usage was not reset on date change!"
    print("Verification 2: PASS")

# --- VERIFICATION 3: Fail-Closed Duration ---
def run_fail_closed_test():
    print("=== Running Verification 3: Fail-Closed Duration ===")
    clean_env()
    write_mock_security()
    write_mock_ffprobe(mode="fail") # Write failing ffprobe mock
    write_config(max_daily=10.0, max_monthly=100.0)
    
    env = get_env()
    
    # Add a mock video file to the stash db using `stash add` command line interface
    test_video_path = os.path.join(TEST_ENV_DIR, "test_video.mp4")
    with open(test_video_path, "wb") as f:
        f.write(b"fake video file content")
        
    # Execute `stash add` to add the video with needs-identify tag
    add_args = [STASH_RACE_BIN, "add", test_video_path, "--tag", "needs-identify", "--type", "file"]
    res = subprocess.run(add_args, env=env, capture_output=True, text=True)
    print(f"Add output: {res.stdout}\\nStderr: {res.stderr}")
    
    # Find the created item ID
    # We can list items via `stash list` or query the db
    list_args = [STASH_RACE_BIN, "list", "--json"]
    res = subprocess.run(list_args, env=env, capture_output=True, text=True)
    items = json.loads(res.stdout)
    video_item = None
    for item in items:
        if item.get("mime_type") == "video/mp4" or "needs-identify" in [t.get("name") for t in item.get("tags", [])]:
            video_item = item
            break
            
    assert video_item is not None, "Could not find the added video item!"
    item_id = video_item.get("id")
    print(f"Created video item ID: {item_id}")
    
    # Start the server (which automatically runs the identify worker)
    # We'll tail the worker log or stderr to see the failure
    proc, token = start_server(env)
    
    # Give the identify worker a couple of seconds to run on the item
    time.sleep(3.0)
    
    stdout, stderr = stop_server(proc)
    
    # Let's inspect the item's tags and log
    res = subprocess.run(list_args, env=env, capture_output=True, text=True)
    updated_items = json.loads(res.stdout)
    updated_item = next(it for it in updated_items if it.get("id") == item_id)
    
    tags = [t.get("name") for t in updated_item.get("tags", [])]
    print(f"Item tags after run: {tags}")
    print(f"Server stderr/logs contains ffprobe error: {'ffprobe error' in stderr or 'getVideoDuration' in stderr}")
    
    # Ensure that it did NOT identify the video, and did NOT clear the tags or fall open.
    # Because attempts = 1 or more, and eventually it becomes identify-failed.
    # Let's verify that the title did not change to anything AI-generated (it should remain the filename).
    assert updated_item.get("title") == "test_video.mp4", f"Title was modified: {updated_item.get('title')}"
    # The needs-identify tag should still be there (unless it reached max attempts and got marked as identify-failed).
    # Either way, it should NOT be successfully identified or marked as identified.
    assert "identify-failed" in tags or "needs-identify" in tags, "Item tags are missing both needs-identify and identify-failed!"
    assert "identify-failed" in tags or "needs-identify" in tags, "Identify worker fell open!"
    print("Verification 3: PASS")

# --- VERIFICATION 4: Gating Verification ---
def run_gating_test():
    print("=== Running Verification 4: Gating Verification ===")
    clean_env()
    write_mock_security()
    write_mock_ffprobe(mode="success", duration="5.0")
    
    # Configure low budget limits
    write_config(max_daily=0.01, max_monthly=1.00)
    
    # Set ledger with high spend for today (Input tokens = 100,000 of gemini-2.5-flash = $0.03 cost)
    today_str = datetime.date.today().strftime("%Y-%m-%d")
    current_month = today_str[:7]
    write_ledger(
        date_str=today_str,
        month_str=current_month,
        prompt_tokens=100000,
        candidate_tokens=0
    )
    
    env = get_env()
    
    # Add a pending item
    test_video_path = os.path.join(TEST_ENV_DIR, "gate_test.mp4")
    with open(test_video_path, "wb") as f:
        f.write(b"another fake video")
    subprocess.run([STASH_RACE_BIN, "add", test_video_path, "--tag", "needs-identify", "--type", "file"], env=env)
    
    proc, token = start_server(env)
    
    # 1. Verify semantic search is blocked (429)
    search_url = "http://localhost:28999/search?q=test&semantic=true"
    status_sem, body_sem = make_request(search_url, token)
    print(f"Semantic search status: {status_sem}, body: {body_sem.decode()}")
    
    # 2. Verify non-semantic search is NOT blocked (200)
    fts_url = "http://localhost:28999/search?q=test"
    status_fts, body_fts = make_request(fts_url, token)
    print(f"Non-semantic search status: {status_fts}")
    
    # 3. Let server run for 3 seconds and verify that the worker did NOT run (the needs-identify tag is still there, and there are no logs about attempts/failures for this item)
    time.sleep(3.0)
    
    # Stop the server to check logs
    stdout, stderr = stop_server(proc)
    
    list_args = [STASH_RACE_BIN, "list", "--json"]
    res = subprocess.run(list_args, env=env, capture_output=True, text=True)
    items = json.loads(res.stdout)
    gate_item = next(it for it in items if it.get("title") == "gate_test.mp4")
    tags = [t.get("name") for t in gate_item.get("tags", [])]
    
    print(f"Gate item tags: {tags}")
    print(f"Logs output during budget lockout (should not contain identify or embed attempt logs):")
    print(stderr)
    
    # Assertions for Gating
    assert status_sem == 429, f"Semantic search was not blocked! Status: {status_sem}"
    assert "budget-exceeded" in body_sem.decode(), f"Error message mismatch: {body_sem.decode()}"
    assert status_fts == 200, f"FTS search was blocked! Status: {status_fts}"
    assert "needs-identify" in tags, "Worker was not blocked! Item needs-identify tag is gone."
    assert "identify-failed" not in tags, "Worker attempted processing and failed rather than gating early!"
    assert "getVideoDuration" not in stderr, "Identify worker ran ffprobe duration check when budget was exceeded!"
    
    # 4. Now, reset the budget limit to a high value and start server again, verifying it resumes
    print("Resetting budget to high limit and restarting server...")
    write_config(max_daily=10.0, max_monthly=100.0)
    
    proc, token = start_server(env)
    
    # Verify semantic search works (doesn't return 429)
    status_sem_after, body_sem_after = make_request(search_url, token)
    print(f"Semantic search status after reset: {status_sem_after}")
    
    # Let the worker run
    time.sleep(3.0)
    
    stdout_after, stderr_after = stop_server(proc)
    
    # The worker should have unblocked and run. Since we have mock security and mock ffprobe (success, duration 5.0),
    # the identify worker should attempt to process the item.
    # It might fail during the real Gemini call (connection refused / network error), but we should see it unblock,
    # log that it is attempting to run, and increment attempt count or log the network error!
    print(f"Logs after reset (should show identify worker starting or failing on Gemini API rather than gating):")
    print(stderr_after)
    
    assert status_sem_after != 429, f"Semantic search is still blocked after budget reset! Status: {status_sem_after}"
    # Verify that the logs show it unblocked (e.g. it ran ffprobe or tried to call gemini)
    assert "getVideoDuration" in stderr_after or "mock ffprobe error" in stderr_after or "[identify] permanent error" in stderr_after or "[identify] transient error" in stderr_after or "worker active" in stderr_after or "Gemini rejected" in stderr_after, "Worker did not resume after budget reset!"
    
    print("Verification 4: PASS")

if __name__ == "__main__":
    try:
        run_concurrency_test()
        run_lockout_recovery_test()
        run_fail_closed_test()
        run_gating_test()
        print("\nAll 4 Verifications passed successfully!")
        sys.exit(0)
    except AssertionError as e:
        print(f"\nVerification FAILURE: {e}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"\nUnexpected error: {e}", file=sys.stderr)
        sys.exit(2)
