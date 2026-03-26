//! Streamline engine — manages all streams for one streamline structure.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::streamline::StreamAssignmentMode;
use crate::tree::unilevel::UnilevelTree;

/// A single stream in the streamline structure.
#[derive(Debug, Serialize, Deserialize)]
pub struct Stream {
    /// Stream ID (1-based, sequential).
    pub id: u32,
    /// The width-1 UnilevelTree for this stream.
    pub tree: UnilevelTree,
    /// The user who owns this stream (earned via rank).
    pub owner_id: Uuid,
    /// Whether this stream is frozen due to rank demotion.
    pub frozen: bool,
    /// When this stream was created.
    pub created_at: i64,
}

/// Engine configuration for stream assignment and lifecycle.
///
/// Separate from StreamlineCommissionConfig. This controls placement
/// and stream management, not commission calculation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamlineConfig {
    /// How new enrollees are assigned to streams.
    pub assignment_mode: StreamAssignmentMode,
    /// Whether sponsors can choose a stream for each enrollment.
    pub enrollment_stream_choice: bool,
    /// Whether excess streams freeze on demotion (vs destroyed).
    pub freeze_on_demotion: bool,
}

/// Manages all streams for a single streamline structure.
///
/// Tracks stream membership, ownership, and lifecycle.
/// Does NOT implement TreeNavigator because streamline's
/// one-user-to-many-positions model violates the single-position
/// assumption. Individual streams are UnilevelTrees that do
/// implement TreeNavigator.
#[derive(Debug, Serialize, Deserialize)]
pub struct StreamlineEngine {
    /// All streams keyed by stream ID.
    streams: HashMap<u32, Stream>,

    /// Maps each user to all streams they have a position on.
    user_streams: HashMap<Uuid, Vec<u32>>,

    /// Maps each user to the streams they own.
    stream_owners: HashMap<Uuid, Vec<u32>>,

    /// Next stream ID to allocate.
    next_stream_id: u32,

    /// Engine configuration.
    config: StreamlineConfig,
}

impl StreamlineEngine {
    /// Creates a new engine with one empty initial stream.
    ///
    /// The initial stream has no owner yet. The first member added
    /// becomes the bootstrap case.
    pub fn new(config: StreamlineConfig, timestamp: i64) -> Self {
        let initial_stream = Stream {
            id: 1,
            tree: UnilevelTree::new(),
            owner_id: Uuid::nil(),
            frozen: false,
            created_at: timestamp,
        };
        let mut streams = HashMap::new();
        streams.insert(1, initial_stream);

        Self {
            streams,
            user_streams: HashMap::new(),
            stream_owners: HashMap::new(),
            next_stream_id: 2,
            config,
        }
    }

    /// Returns the number of streams.
    pub fn stream_count(&self) -> usize {
        self.streams.len()
    }

    /// Returns a reference to a stream by ID.
    pub fn get_stream(&self, stream_id: u32) -> Option<&Stream> {
        self.streams.get(&stream_id)
    }
}
