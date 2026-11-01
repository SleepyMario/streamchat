-- Loaded by OBS Tools > Scripts. Uses the authenticated OBS websocket profile
-- parameter API as a mailbox; never starts/stops outputs or switches scenes.
obs = obslua
local ffi = require('ffi')
ffi.cdef[[char *obs_frontend_get_current_profile_path(void); void bfree(void *ptr);]]
local frontend = ffi.load('obs-frontend-api')
local core = ffi.load('obs')
local function profile_path()
    local ptr = frontend.obs_frontend_get_current_profile_path()
    if ptr == nil then error('Current profile path unavailable') end
    local path = ffi.string(ptr); core.bfree(ptr); return path
end
local section = 'StreamchatBitrate'
local last_request = ''
local function config() return obs.obs_frontend_get_profile_config() end
local function json(value) local d = obs.obs_data_create_from_json(value); return d end
local function state()
    local cfg = config()
    local path = profile_path() .. '/streamEncoder.json'
    local settings = obs.obs_data_create_from_json_file(path)
    local kbps = 0
    if settings then kbps = obs.obs_data_get_int(settings, 'bitrate'); obs.obs_data_release(settings) end
    local out = obs.obs_frontend_get_streaming_output()
    local live = obs.obs_frontend_streaming_active()
    if out then
        local enc = obs.obs_output_get_video_encoder(out)
        if enc and obs.obs_encoder_active(enc) then
            local s = obs.obs_encoder_get_settings(enc)
            kbps = obs.obs_data_get_int(s, 'bitrate'); obs.obs_data_release(s)
        end
        obs.obs_output_release(out)
    end
    obs.config_set_string(cfg, section, 'State', string.format('{"kbps":%d,"live":%s,"timestamp":%d}', kbps, tostring(live), os.time()))
end
local function apply(kbps)
    if kbps ~= 5000 and kbps ~= 6000 and kbps ~= 8000 then error('Unsupported bitrate') end
    local cfg = config()
    if obs.config_get_string(cfg, 'Output', 'Mode') ~= 'Advanced' then error('Advanced output mode is required') end
    if obs.config_get_bool(cfg, 'Output', 'DynamicBitrate') then error('Disable automatic dynamic bitrate before manual adjustment') end
    local path = profile_path() .. '/streamEncoder.json'
    local saved = obs.obs_data_create_from_json_file(path)
    if not saved then error('Cannot read streaming encoder settings') end
    if obs.obs_data_get_string(saved, 'rate_control') ~= 'CBR' then obs.obs_data_release(saved); error('CBR encoder is required') end
    local out = obs.obs_frontend_get_streaming_output()
    local enc = out and obs.obs_output_get_video_encoder(out) or nil
    local function fail(message)
        obs.obs_data_release(saved)
        if out then obs.obs_output_release(out) end
        error(message)
    end
    if obs.obs_frontend_streaming_active() and not enc then fail('Streaming encoder unavailable') end
    if enc and obs.obs_encoder_active(enc) then
        if bit.band(obs.obs_encoder_get_caps(enc), 4 --[[ OBS_ENCODER_CAP_DYN_BITRATE, obs-encoder.h ]]) == 0 then fail('Encoder does not support live bitrate changes') end
        if obs.obs_output_get_video_encoder2(out, 1) then fail('Multiple streaming encoders are not supported') end
        for _, getter in ipairs({obs.obs_frontend_get_recording_output, obs.obs_frontend_get_replay_buffer_output}) do
            local other = getter()
            if other then
                local shared = obs.obs_output_active(other) and obs.obs_output_get_video_encoder(other) == enc
                obs.obs_output_release(other)
                if shared then fail('Recording or replay shares the streaming encoder') end
            end
        end
    end
    local previous = obs.obs_data_get_int(saved, 'bitrate')
    obs.obs_data_set_int(saved, 'bitrate', kbps)
    if not obs.obs_data_save_json_safe(saved, path, 'tmp', 'bak') then fail('Cannot save streaming bitrate') end
    if enc then
        local change = obs.obs_data_create()
        obs.obs_data_set_int(change, 'bitrate', kbps)
        obs.obs_encoder_update(enc, change)
        obs.obs_data_release(change)
        local actual = obs.obs_encoder_get_settings(enc)
        local accepted = obs.obs_data_get_int(actual, 'bitrate') == kbps
        obs.obs_data_release(actual)
        if not accepted then
            obs.obs_data_set_int(saved, 'bitrate', previous)
            obs.obs_data_save_json_safe(saved, path, 'tmp', 'bak')
            fail('Encoder did not accept the requested bitrate')
        end
    end
    obs.obs_data_release(saved)
    if out then obs.obs_output_release(out) end
end
function tick()
    local cfg = config()
    local raw = obs.config_get_string(cfg, section, 'Request') or ''
    if raw ~= '' and raw ~= last_request then
        last_request = raw
        local request = json(raw)
        if request then
            local id = obs.obs_data_get_string(request, 'id')
            local kbps = obs.obs_data_get_int(request, 'kbps')
            local expires = obs.obs_data_get_int(request, 'expires')
            local ok, err = false, 'Expired bitrate request; try again'
            if expires >= os.time() and expires <= os.time() + 30 then ok, err = pcall(apply, kbps) end
            local result = obs.obs_data_create()
            obs.obs_data_set_string(result, 'id', id)
            obs.obs_data_set_int(result, 'kbps', kbps)
            obs.obs_data_set_bool(result, 'ok', ok)
            obs.obs_data_set_bool(result, 'live', obs.obs_frontend_streaming_active())
            if not ok then obs.obs_data_set_string(result, 'error', tostring(err)) end
            obs.config_set_string(cfg, section, 'Result', obs.obs_data_get_json(result))
            obs.obs_data_release(result); obs.obs_data_release(request)
        end
    end
    state()
end
function script_description() return 'Streamchat: independent 5000 / 6000 / 8000 kbps controls. Changes streaming encoder only; preserves scenes and output state.' end
function script_load(settings)
    last_request = obs.config_get_string(config(), section, 'Request') or ''
    obs.timer_add(tick, 250)
end
function script_unload() obs.timer_remove(tick) end
